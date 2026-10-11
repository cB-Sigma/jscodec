package jscodec

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"
	"runtime/metrics"
	"time"
)

// RunWorker serves one codec over stdin and stdout until stdin closes and
// returns the exit code. main calls it when the first argument is
// WorkerCommand; nothing else may write to stdout in that process.
func RunWorker() int {
	return serveWorker(os.Stdin, os.Stdout, nil)
}

// RunWorkerWith is RunWorker with the worker's log attributes passed
// through replace (a redaction hook, say) before they are written to
// stderr as JSON.
func RunWorkerWith(replace func(groups []string, a slog.Attr) slog.Attr) int {
	return serveWorker(os.Stdin, os.Stdout, replace)
}

func serveWorker(stdin io.Reader, stdout io.Writer, replace func([]string, slog.Attr) slog.Attr) int {
	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level, ReplaceAttr: replace}))
	slog.SetDefault(log)
	debug.SetMaxStack(64 << 20)
	in := bufio.NewReader(stdin)
	var c *Codec
	for {
		var req wireReq
		if err := readFrame(in, &req); err != nil {
			if errors.Is(err, io.EOF) {
				return 0
			}
			fmt.Fprintln(os.Stderr, "codec worker:", err)
			return exitProtocol
		}
		resp := &wireResp{}
		switch req.Op {
		case "load":
			if req.Debug {
				level.Set(slog.LevelDebug)
			}
			cc, err := workerLoad(c, &req, log)
			if err != nil {
				resp.Err = toWireErr(err)
				break
			}
			c = cc
			resp.HasDecode, resp.HasEncode, resp.HasFrame = c.hasDecode, c.hasEncode, c.hasFrame
			resp.HasDecodeDown, resp.HasNormalize = c.hasDecodeDown, c.hasNormalize
		case "decode", "encode", "decodeDown", "normalize", "frame":
			if c == nil {
				resp.Err = toWireErr(errors.New("the codec worker has no script loaded"))
				break
			}
			workerCall(c, &req, resp)
		case "probe": // tests: what the sandbox lets the worker do
			resp.N = sandboxProbe()
		case "crash": // tests: an abrupt exit in the middle of a call
			os.Exit(3)
		default:
			resp.Err = toWireErr(fmt.Errorf("the codec worker does not know %q", req.Op))
		}
		resp.CPU = cpuTime()
		resp.PeakRSS = peakRSS()
		if err := writeFrame(stdout, resp); err != nil {
			return exitProtocol
		}
	}
}

func workerLoad(c *Codec, req *wireReq, log *slog.Logger) (*Codec, error) {
	if c != nil {
		return nil, errors.New("the codec worker has a script loaded already")
	}
	l := req.Limits
	if l == nil {
		return nil, errors.New("the codec worker needs limits")
	}
	lim := Limits{Timeout: l.Timeout, MaxMemory: l.MaxMemory, MaxStack: l.MaxStack, MaxArray: l.MaxArray,
		MaxString: l.MaxString, MaxBuffer: l.MaxBuffer, Isolation: InProcess, KeepState: l.KeepState, NodeBuffer: l.NodeBuffer}
	if l.HardOnly {
		lim.Timeout, lim.MaxMemory = time.Hour, 1<<50
	}
	if l.ProcessMemory > 0 {
		debug.SetMemoryLimit(l.ProcessMemory * 3 / 4)
		go memoryWatchdog(l.ProcessMemory)
		setDataLimit(2*l.ProcessMemory + 128<<20)
	}
	if l.CPUSeconds > 0 {
		setCPULimit(l.CPUSeconds)
	}
	c, err := New(req.Name, req.Source, lim, log)
	if err == nil && l.Seccomp {
		if err = installSeccomp(); err != nil {
			err = fmt.Errorf("the codec worker could not install its system call filter: %w", err)
		}
	}
	return c, err
}

func workerCall(c *Codec, req *wireReq, resp *wireResp) {
	ctx := context.Background()
	var err error
	switch req.Op {
	case "decode":
		if req.Uplink == nil {
			err = errors.New("the codec worker got no uplink")
			break
		}
		u := *req.Uplink
		u.Variables, u.Extra = numMap(u.Variables), numMap(u.Extra)
		resp.Decoded, err = c.Decode(ctx, u)
	case "encode":
		if req.Downlink == nil {
			err = errors.New("the codec worker got no downlink")
			break
		}
		d := *req.Downlink
		d.Data, d.Variables, d.Extra = numbers(d.Data), numMap(d.Variables), numMap(d.Extra)
		resp.Encoded, err = c.Encode(ctx, d)
	case "decodeDown":
		if req.Frame == nil {
			err = errors.New("the codec worker got no downlink frame")
			break
		}
		f := *req.Frame
		f.Variables, f.Extra = numMap(f.Variables), numMap(f.Extra)
		resp.Decoded, err = c.DecodeDownlink(ctx, f)
	case "normalize":
		resp.Normalized, err = c.Normalize(ctx, numMap(req.Data))
	case "frame":
		resp.N, err = c.FrameLength(ctx, req.Bytes)
	}
	resp.Err = toWireErr(err)
}

// memoryWatchdog exits the worker once the memory the Go runtime holds
// from the operating system passes max. It runs on its own goroutine, which
// the script cannot block: goja code is preemptible Go code.
func memoryWatchdog(max int64) {
	s := []metrics.Sample{{Name: "/memory/classes/total:bytes"}, {Name: "/memory/classes/heap/released:bytes"}}
	t := time.NewTicker(2 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		metrics.Read(s)
		if s[0].Value.Kind() != metrics.KindUint64 || s[1].Value.Kind() != metrics.KindUint64 {
			continue
		}
		if held := int64(s[0].Value.Uint64() - s[1].Value.Uint64()); held > max {
			fmt.Fprintf(os.Stderr, "codec worker: memory held %d bytes, past the limit of %d\n", held, max)
			os.Exit(exitMemory)
		}
	}
}
