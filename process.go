package jscodec

// Process isolation. The parent (this file) keeps a pool of worker
// processes per codec; each worker is this binary started with the hidden
// WorkerCommand argument (worker.go) and runs the script with the in-call
// limits of the in-process sandbox. On top of those, hard limits that the
// script cannot defeat:
//
//   - a per-call deadline in the parent (twice Timeout plus 250ms); a
//     worker that has not answered by then is sent SIGKILL;
//   - memory: the worker sets Go's soft memory limit, a watchdog in the
//     worker exits it once the memory the Go runtime holds passes
//     ProcessMemory, on Linux RLIMIT_DATA is set as a kernel backstop and
//     the parent kills a worker whose resident size runs past the ceiling;
//   - CPU: RLIMIT_CPU caps a worker's lifetime CPU; the parent retires a
//     worker before it gets near the cap;
//   - the worker starts with an empty environment (no secrets) and exits
//     when its stdin closes; on Linux it is also killed with the parent.
//
// A killed or crashed worker is discarded and the next call starts a
// fresh one. Requests and responses are JSON documents framed by a 4-byte
// big-endian length over the worker's stdin and stdout.

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// WorkerCommand is the hidden first argument that turns the binary into a
// codec worker; main must call RunWorker when it sees it.
const WorkerCommand = "codec-worker"

// Isolation selects where a codec's script runs.
type Isolation string

// Isolation modes.
const (
	InProcess Isolation = "inprocess"
	Process   Isolation = "process"
)

// Errors specific to process isolation.
var (
	ErrWorkerCrashed = errors.New("the codec worker process stopped unexpectedly")
	ErrClosed        = errors.New("the codec is closed")
)

// Worker exit codes the parent understands.
const (
	exitMemory   = 87
	exitCPU      = 88
	exitProtocol = 89
)

const maxFrame = 16 << 20

// Sandbox is the isolation part of a codec's configuration, for IPE
// settings to embed.
type Sandbox struct {
	Isolation     Isolation `json:"isolation,omitempty"`     // inprocess (default) or process
	Workers       int       `json:"workers,omitempty"`       // worker processes (default 2)
	ProcessMemory int64     `json:"processMemory,omitempty"` // bytes per worker (default 64 MiB + 4 × memory)
}

// Apply copies the isolation settings into l.
func (s Sandbox) Apply(l Limits) Limits {
	l.Isolation, l.Workers, l.ProcessMemory = s.Isolation, s.Workers, s.ProcessMemory
	return l
}

// ---- wire format ------------------------------------------------------------

type wireLimits struct {
	Timeout       time.Duration `json:"timeout"`
	MaxMemory     int64         `json:"maxMemory"`
	MaxStack      int           `json:"maxStack"`
	MaxArray      int           `json:"maxArray"`
	MaxString     int           `json:"maxString"`
	MaxBuffer     int           `json:"maxBuffer"`
	ProcessMemory int64         `json:"processMemory"`
	CPUSeconds    uint64        `json:"cpuSeconds"`
	HardOnly      bool          `json:"hardOnly,omitempty"`
	KeepState     bool          `json:"keepState,omitempty"`
	NodeBuffer    bool          `json:"nodeBuffer,omitempty"`
	Seccomp       bool          `json:"seccomp,omitempty"`
}

type wireReq struct {
	Op       string         `json:"op"` // load, decode, encode, decodeDown, normalize, frame, crash (tests)
	Name     string         `json:"name,omitempty"`
	Source   string         `json:"source,omitempty"`
	Limits   *wireLimits    `json:"limits,omitempty"`
	Debug    bool           `json:"debug,omitempty"`
	Uplink   *Uplink        `json:"uplink,omitempty"`
	Downlink *Downlink      `json:"downlink,omitempty"`
	Frame    *DownlinkFrame `json:"frame,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	Bytes    []byte         `json:"bytes,omitempty"`
}

type wireErr struct {
	Kind   string         `json:"kind"`
	Msg    string         `json:"msg"`
	Errors []string       `json:"errors,omitempty"`
	Result map[string]any `json:"result,omitempty"`
}

type wireResp struct {
	Err           *wireErr      `json:"err,omitempty"`
	HasDecode     bool          `json:"hasDecode,omitempty"`
	HasEncode     bool          `json:"hasEncode,omitempty"`
	HasFrame      bool          `json:"hasFrame,omitempty"`
	HasDecodeDown bool          `json:"hasDecodeDown,omitempty"`
	HasNormalize  bool          `json:"hasNormalize,omitempty"`
	Decoded       *Decoded      `json:"decoded,omitempty"`
	Encoded       *Encoded      `json:"encoded,omitempty"`
	Normalized    *Normalized   `json:"normalized,omitempty"`
	N             int           `json:"n,omitempty"`
	CPU           time.Duration `json:"cpu,omitempty"` // the worker's CPU time so far
	PeakRSS       int64         `json:"peakRSS,omitempty"`
}

var errKinds = map[string]error{
	"timeout": ErrTimeout, "memory": ErrMemory, "canceled": ErrCanceled,
	"nodecoder": ErrNoDecoder, "noencoder": ErrNoEncoder,
	"nodowndecoder": ErrNoDownlinkDecoder, "nonormalizer": ErrNoNormalizer,
}

func toWireErr(err error) *wireErr {
	if err == nil {
		return nil
	}
	var de *DecoderError
	if errors.As(err, &de) {
		return &wireErr{Kind: "decoder", Msg: err.Error(), Errors: de.Errors, Result: de.Result}
	}
	for k, e := range errKinds {
		if errors.Is(err, e) {
			return &wireErr{Kind: k, Msg: err.Error()}
		}
	}
	return &wireErr{Kind: "other", Msg: err.Error()}
}

// remoteError keeps the worker's message and the sentinel it wraps.
type remoteError struct {
	msg string
	is  error
}

func (e *remoteError) Error() string { return e.msg }
func (e *remoteError) Unwrap() error { return e.is }

func (w *wireErr) err() error {
	if w.Kind == "decoder" {
		return &DecoderError{Errors: w.Errors, Result: numMap(w.Result)}
	}
	if s, ok := errKinds[w.Kind]; ok {
		if w.Msg == s.Error() {
			return s
		}
		return &remoteError{msg: w.Msg, is: s}
	}
	return errors.New(w.Msg)
}

func writeFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > maxFrame {
		return fmt.Errorf("a codec message of %d bytes is larger than %d", len(b), maxFrame)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(append(hdr[:], b...)); err != nil {
		return err
	}
	return nil
}

func readFrame(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrame {
		return fmt.Errorf("a codec message of %d bytes is larger than %d", n, maxFrame)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	return d.Decode(v)
}

// numbers turns json.Number values back into int64 (integers) or float64,
// the types goja exports, so both isolation modes hand out the same types.
func numbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	case map[string]any:
		for k, e := range x {
			x[k] = numbers(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = numbers(e)
		}
		return x
	}
	return v
}

func numMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return numbers(m).(map[string]any)
}

// ---- pool -------------------------------------------------------------------

type procPool struct {
	name, source string
	lim          Limits
	log          *slog.Logger
	cpuSeconds   uint64

	sem  chan struct{} // one slot per worker
	idle chan *worker

	mu     sync.Mutex
	closed bool
	all    map[*worker]struct{}
	usage  Usage
}

// account adds a worker's report to the pool's usage.
func (p *procPool) account(r *wireResp) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.usage.Calls++
	p.usage.PeakRSS = max(p.usage.PeakRSS, r.PeakRSS)
	p.usage.WorkerCPU = max(p.usage.WorkerCPU, r.CPU)
}

func newProcess(name, source string, lim Limits, log *slog.Logger) (*Codec, error) {
	if !processSupported {
		return nil, errors.New("codec process isolation is not supported on this platform")
	}
	if (lim.Namespaces || lim.Seccomp) && runtime.GOOS != "linux" {
		return nil, errors.New("codec namespaces and seccomp are available on Linux only")
	}
	// The CPU cap covers a worker's life; workers retire at 80% of it.
	cpu := max(60*time.Second, 600*lim.Timeout)
	p := &procPool{name: name, source: source, lim: lim, log: log, cpuSeconds: uint64(cpu / time.Second),
		sem: make(chan struct{}, lim.Workers), idle: make(chan *worker, lim.Workers), all: map[*worker]struct{}{}}
	// The first worker loads the script and says which functions it has.
	p.sem <- struct{}{}
	w, resp, err := p.spawn(context.Background())
	if err != nil {
		<-p.sem
		return nil, err
	}
	p.release(w, true)
	c := &Codec{name: name, lim: lim, log: log, proc: p,
		hasDecode: resp.HasDecode, hasEncode: resp.HasEncode, hasFrame: resp.HasFrame,
		hasDecodeDown: resp.HasDecodeDown, hasNormalize: resp.HasNormalize}
	// Workers of a codec nobody holds any more are stopped.
	runtime.AddCleanup(c, func(p *procPool) { p.close() }, p)
	return c, nil
}

func (p *procPool) close() {
	p.mu.Lock()
	p.closed = true
	ws := make([]*worker, 0, len(p.all))
	for w := range p.all {
		ws = append(ws, w)
	}
	p.mu.Unlock()
	for _, w := range ws {
		w.kill()
	}
}

// pids lists the running workers (tests).
func (p *procPool) pids() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []int
	for w := range p.all {
		out = append(out, w.cmd.Process.Pid)
	}
	return out
}

func (p *procPool) acquire(ctx context.Context) (*worker, error) {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ErrCanceled
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		<-p.sem
		return nil, ErrClosed
	}
	for {
		select {
		case w := <-p.idle:
			if w.exited() {
				p.forget(w)
				continue
			}
			return w, nil
		default:
		}
		break
	}
	w, _, err := p.spawn(ctx)
	if err != nil {
		<-p.sem
		return nil, err
	}
	return w, nil
}

// release returns w to the pool when keep, else stops it; either way its
// slot is freed.
func (p *procPool) release(w *worker, keep bool) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if keep && !closed && !w.exited() && w.cpu < time.Duration(p.cpuSeconds)*time.Second*8/10 {
		p.idle <- w
	} else {
		w.kill()
		p.forget(w)
	}
	<-p.sem
}

func (p *procPool) forget(w *worker) {
	p.mu.Lock()
	delete(p.all, w)
	p.mu.Unlock()
}

func (p *procPool) spawn(ctx context.Context) (*worker, *wireResp, error) {
	w, err := startWorker(p.log, p.name, p.lim.Namespaces)
	if err != nil {
		return nil, nil, err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		w.kill()
		return nil, nil, ErrClosed
	}
	p.all[w] = struct{}{}
	p.mu.Unlock()
	l := p.lim
	req := &wireReq{Op: "load", Name: p.name, Source: p.source, Debug: p.log.Enabled(ctx, slog.LevelDebug),
		Limits: &wireLimits{Timeout: l.Timeout, MaxMemory: l.MaxMemory, MaxStack: l.MaxStack, MaxArray: l.MaxArray,
			MaxString: l.MaxString, MaxBuffer: l.MaxBuffer, ProcessMemory: l.ProcessMemory, CPUSeconds: p.cpuSeconds, KeepState: l.KeepState, NodeBuffer: l.NodeBuffer, Seccomp: l.Seccomp,
			HardOnly: l.hardOnly}}
	// Starting the binary is included in the load deadline.
	resp, err := w.roundTrip(ctx, req, 10*time.Second+p.hardDeadline(), p.rssLimit())
	if err == nil && resp.Err != nil {
		err = resp.Err.err()
	}
	if err != nil {
		w.kill()
		p.forget(w)
		return nil, nil, err
	}
	return w, resp, nil
}

func (p *procPool) hardDeadline() time.Duration { return 2*p.lim.Timeout + 250*time.Millisecond }

func (p *procPool) rssLimit() int64 { return p.lim.ProcessMemory + 128<<20 }

func (p *procPool) do(ctx context.Context, req *wireReq) (*wireResp, error) {
	w, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := w.roundTrip(ctx, req, p.hardDeadline(), p.rssLimit())
	if err == nil {
		w.cpu = resp.CPU
		p.account(resp)
	}
	p.release(w, err == nil)
	if err != nil {
		return nil, err
	}
	if resp.Err != nil {
		return nil, resp.Err.err()
	}
	return resp, nil
}

func (p *procPool) decode(ctx context.Context, u Uplink) (*Decoded, error) {
	resp, err := p.do(ctx, &wireReq{Op: "decode", Uplink: &u})
	if err != nil {
		return nil, err
	}
	return workerDecoded(resp)
}

func workerDecoded(resp *wireResp) (*Decoded, error) {
	d := resp.Decoded
	if d == nil {
		return nil, errors.New("the codec worker returned no result")
	}
	d.Data = numMap(d.Data)
	for i, e := range d.Events {
		d.Events[i] = numMap(e)
	}
	d.Result = numMap(d.Result)
	return d, nil
}

func (p *procPool) decodeDownlink(ctx context.Context, f DownlinkFrame) (*Decoded, error) {
	resp, err := p.do(ctx, &wireReq{Op: "decodeDown", Frame: &f})
	if err != nil {
		return nil, err
	}
	return workerDecoded(resp)
}

func (p *procPool) normalize(ctx context.Context, data map[string]any) (*Normalized, error) {
	resp, err := p.do(ctx, &wireReq{Op: "normalize", Data: data})
	if err != nil {
		return nil, err
	}
	n := resp.Normalized
	if n == nil {
		return nil, errors.New("the codec worker returned no result")
	}
	for i, e := range n.Data {
		n.Data[i] = numMap(e)
	}
	n.Result = numMap(n.Result)
	return n, nil
}

func (p *procPool) encode(ctx context.Context, dl Downlink) (*Encoded, error) {
	resp, err := p.do(ctx, &wireReq{Op: "encode", Downlink: &dl})
	if err != nil {
		return nil, err
	}
	if resp.Encoded == nil {
		return nil, errors.New("the codec worker returned no result")
	}
	resp.Encoded.Result = numMap(resp.Encoded.Result)
	return resp.Encoded, nil
}

func (p *procPool) frameLength(ctx context.Context, buf []byte) (int, error) {
	resp, err := p.do(ctx, &wireReq{Op: "frame", Bytes: buf})
	if err != nil {
		return 0, err
	}
	return resp.N, nil
}

// ---- one worker -------------------------------------------------------------

type worker struct {
	cmd    *exec.Cmd
	in     *os.File
	out    *bufio.Reader
	outF   *os.File
	stderr *tail
	done   chan struct{} // closed once the process has been reaped
	cpu    time.Duration // last reported CPU time; touched only by the holder

	killOnce sync.Once
}

func startWorker(log *slog.Logger, name string, namespaces bool) (*worker, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("finding this program to start a codec worker: %w", err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, err
	}
	cmd := exec.Command(exe, WorkerCommand)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	// No inherited environment: the worker needs no configuration and must
	// not see the server's secrets.
	cmd.Env = []string{"GOMAXPROCS=2", "GOTRACEBACK=single"}
	cmd.Dir = "/"
	cmd.SysProcAttr = workerSysProcAttr(namespaces)
	err = cmd.Start()
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		errR.Close()
		return nil, fmt.Errorf("starting a codec worker: %w", err)
	}
	w := &worker{cmd: cmd, in: inW, outF: outR, out: bufio.NewReader(outR), stderr: &tail{}, done: make(chan struct{})}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		relayStderr(errR, w.stderr, log, name, cmd.Process.Pid)
		errR.Close()
	}()
	go func() {
		_ = cmd.Wait()
		<-stderrDone
		close(w.done)
	}()
	return w, nil
}

func (w *worker) exited() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

func (w *worker) kill() {
	w.killOnce.Do(func() {
		_ = w.cmd.Process.Kill()
		w.in.Close()
		go func() {
			<-w.done
			w.outF.Close()
		}()
	})
}

type rtResult struct {
	resp *wireResp
	err  error
}

// roundTrip sends req and waits for the answer, killing the worker past
// the deadline, on cancellation or when it outgrows its memory.
func (w *worker) roundTrip(ctx context.Context, req *wireReq, deadline time.Duration, rssMax int64) (*wireResp, error) {
	ch := make(chan rtResult, 1)
	go func() {
		if err := writeFrame(w.in, req); err != nil {
			ch <- rtResult{err: err}
			return
		}
		var r wireResp
		err := readFrame(w.out, &r)
		ch <- rtResult{resp: &r, err: err}
	}()
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	var tick <-chan time.Time
	if rssWatch {
		t := time.NewTicker(10 * time.Millisecond)
		defer t.Stop()
		tick = t.C
	}
	stop := func(err error) (*wireResp, error) {
		w.kill()
		<-ch
		return nil, err
	}
	for {
		select {
		case r := <-ch:
			if r.err != nil {
				return nil, w.failure(r.err)
			}
			return r.resp, nil
		case <-timer.C:
			return stop(fmt.Errorf("%w: the codec worker did not answer within %s and was stopped", ErrTimeout, deadline))
		case <-ctx.Done():
			return stop(ErrCanceled)
		case <-tick:
			if n, ok := processRSS(w.cmd.Process.Pid); ok && n > rssMax {
				return stop(fmt.Errorf("%w: the codec worker's resident memory passed %d bytes and it was stopped", ErrMemory, rssMax))
			}
		}
	}
}

// failure explains why a worker stopped answering.
func (w *worker) failure(ioErr error) error {
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		w.kill()
		<-w.done
	}
	w.kill()
	st := w.cmd.ProcessState
	last := w.stderr.last()
	switch {
	case st != nil && st.ExitCode() == exitMemory, strings.Contains(last, "out of memory"):
		return fmt.Errorf("%w: the codec worker ran past its memory ceiling and was stopped", ErrMemory)
	case st != nil && st.ExitCode() == exitCPU:
		return fmt.Errorf("%w: the codec worker used up its CPU time and was stopped", ErrTimeout)
	}
	if st != nil && cpuSignaled(st) {
		return fmt.Errorf("%w: the codec worker used up its CPU time and was stopped", ErrTimeout)
	}
	desc := "no exit status"
	if st != nil {
		desc = st.String()
	}
	if last != "" {
		desc += ": " + last
	}
	if ioErr != nil && !errors.Is(ioErr, io.EOF) && !errors.Is(ioErr, io.ErrUnexpectedEOF) {
		desc += "; " + ioErr.Error()
	}
	return fmt.Errorf("%w (%s)", ErrWorkerCrashed, desc)
}

// tail keeps the last lines a worker wrote to stderr that were not
// console output.
type tail struct {
	mu    sync.Mutex
	lines []string
}

func (t *tail) add(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(s) > 512 {
		s = s[:512]
	}
	t.lines = append(t.lines, s)
	if len(t.lines) > 8 {
		t.lines = t.lines[len(t.lines)-8:]
	}
}

// last returns the most telling line: a fatal error if there is one.
func (t *tail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, l := range t.lines {
		if strings.HasPrefix(l, "fatal error") || strings.HasPrefix(l, "panic") {
			return l
		}
	}
	if len(t.lines) == 0 {
		return ""
	}
	return t.lines[len(t.lines)-1]
}

// relayStderr passes the worker's console lines to the IPE's debug log and
// keeps everything else for failure reports.
func relayStderr(r io.Reader, t *tail, log *slog.Logger, name string, pid int) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		line := sc.Text()
		var rec struct {
			Msg     string `json:"msg"`
			Message string `json:"message"`
		}
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &rec) == nil && rec.Msg == "codec console" {
			log.Debug("codec console", "codec", name, "message", rec.Message, "worker", pid)
			continue
		}
		if strings.TrimSpace(line) != "" {
			t.add(line)
		}
	}
}

// Usage is what a codec's worker processes reported (Process isolation
// only; one script per worker, so the figures are that script's).
type Usage struct {
	// Calls answered by workers.
	Calls int
	// PeakRSS is the largest peak resident size of any worker, in bytes.
	PeakRSS int64
	// WorkerCPU is the largest CPU time any worker had used.
	WorkerCPU time.Duration
}

// Usage reports the worker processes' figures; in process it is zero.
func (c *Codec) Usage() Usage {
	if c == nil || c.proc == nil {
		return Usage{}
	}
	c.proc.mu.Lock()
	defer c.proc.mu.Unlock()
	return c.proc.usage
}

// probe asks a worker what its sandbox lets it do (tests).
func (p *procPool) probe(ctx context.Context) (int, error) {
	resp, err := p.do(ctx, &wireReq{Op: "probe"})
	if err != nil {
		return 0, err
	}
	return resp.N, nil
}
