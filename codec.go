// Package jscodec runs integrator-written payload codecs in a JavaScript
// sandbox (github.com/dop251/goja). A codec is a script in the style the
// LoRaWAN network servers made familiar:
//
//	function decodeUplink(input) {   // input.bytes: array of 0..255
//	  return { data: { temperature: (input.bytes[0] << 8 | input.bytes[1]) / 10 },
//	           warnings: [], errors: [] };
//	}
//	function encodeDownlink(input) { // input.data: the command object
//	  return { bytes: [1, input.data.on ? 1 : 0], fPort: 10 };
//	}
//
// optionally decodeDownlink(input) (input.bytes of a downlink back to an
// object) and normalizeUplink(input) (input.data, decodeUplink's data, to
// normalized measurements), as The Things Stack names them, and, for
// stream transports, an optional frameLength(bytes) that says how
// long the frame at the start of a buffer is (0: need more bytes, -1: not a
// frame start, drop one byte).
//
// The sandbox has no module loader, no network, no files and no timers;
// console.log goes to the logger given to New, at debug level. Each call
// is bounded:
//
//   - CPU: the call is interrupted after Limits.Timeout; JavaScript loops,
//     recursion (Limits.MaxStack) and callbacks from built-ins stop there.
//   - Built-ins that loop natively (goja cannot interrupt them) are guarded
//     so one call is bounded: arrays and array-likes longer than
//     Limits.MaxArray, strings built by repeat/pad beyond Limits.MaxString,
//     ArrayBuffers and typed arrays beyond Limits.MaxBuffer and JSON of
//     over-long arrays are refused with a RangeError; regular expressions
//     that backtrack time out.
//   - Memory: bytes allocated during the call are sampled every
//     millisecond and the call is interrupted past Limits.MaxMemory. The
//     sample is process-wide, so the cap is a backstop with some
//     overshoot, not an exact per-script quota.
//
// A runtime whose call was interrupted or threw is discarded, so a
// half-finished script never leaks state into the next call.
//
// With Limits.Isolation set to Process the script never runs in this
// process: a pool of worker processes (this binary re-executed with the
// hidden WorkerCommand argument) runs it under the same limits, plus hard
// ones the operating system and the parent enforce. See process.go.
package jscodec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"runtime/metrics"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dlclark/regexp2/v2"
	"github.com/dop251/goja"
)

func init() {
	// goja compiles backtracking regular expressions with regexp2; a match
	// runs natively and cannot be interrupted, so bound it here. Only goja
	// uses regexp2 in this program.
	regexp2.DefaultMatchTimeout = 250 * time.Millisecond
}

// Limits bound one codec call.
type Limits struct {
	Timeout   time.Duration // default 100ms
	MaxMemory int64         // bytes allocated per call; default 32 MiB
	MaxStack  int           // call depth; default 256
	MaxArray  int           // array length for built-ins; default 65536
	MaxString int           // string length from repeat/pad; default 1 MiB
	MaxBuffer int           // ArrayBuffer/typed array bytes; default 1 MiB

	// Isolation selects where the script runs: InProcess (default) or
	// Process (worker processes with hard limits).
	Isolation Isolation
	// Workers is the number of worker processes (Process only); default 2.
	Workers int
	// ProcessMemory is a worker's hard memory ceiling in bytes (Process
	// only); default 64 MiB plus four times MaxMemory.
	ProcessMemory int64

	hardOnly bool // tests: the worker runs without the in-call limits
}

func (l Limits) withDefaults() Limits {
	if l.Timeout <= 0 {
		l.Timeout = 100 * time.Millisecond
	}
	if l.MaxMemory <= 0 {
		l.MaxMemory = 32 << 20
	}
	if l.MaxStack <= 0 {
		l.MaxStack = 256
	}
	if l.MaxArray <= 0 {
		l.MaxArray = 65536
	}
	if l.MaxString <= 0 {
		l.MaxString = 1 << 20
	}
	if l.MaxBuffer <= 0 {
		l.MaxBuffer = 1 << 20
	}
	if l.Isolation == "" {
		l.Isolation = InProcess
	}
	if l.Workers <= 0 {
		l.Workers = 2
	}
	if l.ProcessMemory <= 0 {
		l.ProcessMemory = 64<<20 + 4*l.MaxMemory
	}
	return l
}

// Errors a call can end with.
var (
	ErrTimeout   = errors.New("the codec ran longer than its time limit")
	ErrMemory    = errors.New("the codec allocated more memory than its limit")
	ErrCanceled  = errors.New("the codec call was canceled")
	ErrNoDecoder = errors.New("the codec has no decodeUplink function")
	ErrNoEncoder = errors.New("the codec has no encodeDownlink function")
	// ErrNoDownlinkDecoder and ErrNoNormalizer: the script lacks
	// decodeDownlink or normalizeUplink.
	ErrNoDownlinkDecoder = errors.New("the codec has no decodeDownlink function")
	ErrNoNormalizer      = errors.New("the codec has no normalizeUplink function")
)

// Maximum sizes of what a codec may return.
const (
	maxOutBytes = 64 << 10
	maxDataKeys = 512
)

// Codec is a compiled script. It is safe for concurrent use: calls run on a
// small pool of runtimes.
type Codec struct {
	name string
	prog *goja.Program
	lim  Limits
	log  *slog.Logger
	pool chan *goja.Runtime
	proc *procPool // Process isolation: the worker pool; nil in process

	hasDecode, hasEncode, hasFrame bool
	hasDecodeDown, hasNormalize    bool
}

// New compiles source and checks it defines at least one codec function.
func New(name, source string, lim Limits, log *slog.Logger) (*Codec, error) {
	if log == nil {
		log = slog.Default()
	}
	lim = lim.withDefaults()
	switch lim.Isolation {
	case InProcess:
	case Process:
		return newProcess(name, source, lim, log)
	default:
		return nil, fmt.Errorf("codec isolation %q is not %s or %s", lim.Isolation, InProcess, Process)
	}
	prog, err := goja.Compile(name, source, false)
	if err != nil {
		return nil, fmt.Errorf("the codec script does not compile: %w", err)
	}
	c := &Codec{name: name, prog: prog, lim: lim, log: log, pool: make(chan *goja.Runtime, 4)}
	rt, err := c.newRuntime()
	if err != nil {
		return nil, err
	}
	fn := func(n string) bool { _, ok := goja.AssertFunction(rt.Get(n)); return ok }
	c.hasDecode, c.hasEncode, c.hasFrame = fn("decodeUplink"), fn("encodeDownlink"), fn("frameLength")
	c.hasDecodeDown, c.hasNormalize = fn("decodeDownlink"), fn("normalizeUplink")
	if !c.hasDecode && !c.hasEncode && !c.hasFrame && !c.hasDecodeDown {
		return nil, errors.New("the codec script defines none of decodeUplink, encodeDownlink, decodeDownlink or frameLength")
	}
	c.put(rt)
	return c, nil
}

// HasDecoder, HasEncoder and HasFrameLength report which functions the
// script defines.
func (c *Codec) HasDecoder() bool     { return c.hasDecode }
func (c *Codec) HasEncoder() bool     { return c.hasEncode }
func (c *Codec) HasFrameLength() bool { return c.hasFrame }

// HasDownlinkDecoder and HasNormalizer report whether the script defines
// decodeDownlink and normalizeUplink.
func (c *Codec) HasDownlinkDecoder() bool { return c.hasDecodeDown }
func (c *Codec) HasNormalizer() bool      { return c.hasNormalize }

// Close stops the codec's worker processes (Process isolation); later
// calls fail. It is a no-op in process and on nil.
func (c *Codec) Close() error {
	if c != nil && c.proc != nil {
		c.proc.close()
	}
	return nil
}

// guards replaces the amplifying built-ins with bounded versions. The
// originals stay reachable only through the closure.
const guards = `(function (MAXA, MAXS, MAXB) {
  'use strict';
  var RE = RangeError, apply = Function.prototype.apply;
  function len(o) { if (o === null || o === undefined) return 0; var n = Number(o.length); return n > 0 ? n : 0; }
  function checkA(o) { if (len(o) > MAXA) throw new RE('array longer than the codec sandbox allows'); }
  function wrap(obj, name, check) {
    var f = obj[name];
    if (typeof f !== 'function') return;
    var g = function () { check(this, arguments); return apply.call(f, this, arguments); };
    Object.defineProperty(obj, name, { value: g, writable: true, configurable: true });
  }
  Object.getOwnPropertyNames(Array.prototype).forEach(function (n) {
    if (n !== 'constructor') wrap(Array.prototype, n, function (t) { checkA(t); });
  });
  wrap(Array, 'from', function (t, a) { checkA(a[0]); });
  wrap(Array, 'of', function (t, a) { checkA(a); });
  function checkS(n) { if (n > MAXS) throw new RE('string longer than the codec sandbox allows'); }
  wrap(String.prototype, 'repeat', function (t, a) { checkS(String(t).length * Number(a[0])); });
  wrap(String.prototype, 'padStart', function (t, a) { checkS(Number(a[0])); });
  wrap(String.prototype, 'padEnd', function (t, a) { checkS(Number(a[0])); });
  var stringify = JSON.stringify;
  function walk(v, depth) {
    if (depth > 64) throw new RE('value nested deeper than the codec sandbox allows');
    if (v === null || typeof v !== 'object') return;
    checkA(v);
    if (Array.isArray(v)) { for (var i = 0; i < v.length; i++) walk(v[i], depth + 1); return; }
    for (var k in v) walk(v[k], depth + 1);
  }
  Object.defineProperty(JSON, 'stringify', { value: function (v) { walk(v, 0); return apply.call(stringify, JSON, arguments); }, writable: true, configurable: true });
  function bound(C, per) {
    return new Proxy(C, {
      construct: function (t, a, nt) {
        var x = a[0];
        if (typeof x === 'number' && x * per > MAXB) throw new RE('buffer larger than the codec sandbox allows');
        if (x !== null && typeof x === 'object' && !(x instanceof ArrayBuffer) && len(x) * per > MAXB) throw new RE('buffer larger than the codec sandbox allows');
        return Reflect.construct(t, a, nt);
      },
      apply: function (t, th, a) { return apply.call(t, th, a); }
    });
  }
  var g = this;
  g.ArrayBuffer = bound(ArrayBuffer, 1);
  ['Int8Array', 'Uint8Array', 'Uint8ClampedArray', 'Int16Array', 'Uint16Array', 'Int32Array', 'Uint32Array',
   'Float32Array', 'Float64Array', 'BigInt64Array', 'BigUint64Array'].forEach(function (n) {
    if (g[n]) g[n] = bound(g[n], g[n].BYTES_PER_ELEMENT || 8);
  });
  g.Array = new Proxy(Array, {
    construct: function (t, a, nt) { if (a.length === 1 && typeof a[0] === 'number' && a[0] > MAXA) throw new RE('array longer than the codec sandbox allows'); return Reflect.construct(t, a, nt); },
    apply: function (t, th, a) { if (a.length === 1 && typeof a[0] === 'number' && a[0] > MAXA) throw new RE('array longer than the codec sandbox allows'); return apply.call(t, th, a); }
  });
})`

func (c *Codec) newRuntime() (*goja.Runtime, error) {
	rt := goja.New()
	rt.SetMaxCallStackSize(c.lim.MaxStack)
	g, err := rt.RunString(guards)
	if err != nil {
		return nil, fmt.Errorf("installing the codec sandbox: %w", err)
	}
	fn, _ := goja.AssertFunction(g)
	if _, err := fn(rt.GlobalObject(), rt.ToValue(c.lim.MaxArray), rt.ToValue(c.lim.MaxString), rt.ToValue(c.lim.MaxBuffer)); err != nil {
		return nil, fmt.Errorf("installing the codec sandbox: %w", err)
	}
	console := rt.NewObject()
	logf := func(call goja.FunctionCall) goja.Value {
		parts := make([]string, 0, len(call.Arguments))
		for _, a := range call.Arguments {
			s := a.String()
			if len(s) > 512 {
				s = s[:512] + "…"
			}
			parts = append(parts, s)
		}
		c.log.Debug("codec console", "codec", c.name, "message", strings.Join(parts, " "))
		return goja.Undefined()
	}
	for _, n := range []string{"log", "info", "warn", "error", "debug"} {
		_ = console.Set(n, logf)
	}
	_ = rt.Set("console", console)
	// Top-level code of the script is bounded like a call.
	if _, err := c.guarded(context.Background(), rt, func() (goja.Value, error) { return rt.RunProgram(c.prog) }); err != nil {
		return nil, fmt.Errorf("running the codec script: %w", err)
	}
	return rt, nil
}

func (c *Codec) get() (*goja.Runtime, error) {
	select {
	case rt := <-c.pool:
		return rt, nil
	default:
		return c.newRuntime()
	}
}

func (c *Codec) put(rt *goja.Runtime) {
	select {
	case c.pool <- rt:
	default:
	}
}

var allocSample = []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}}
var allocMu sync.Mutex

func allocated() int64 {
	allocMu.Lock()
	defer allocMu.Unlock()
	metrics.Read(allocSample)
	if allocSample[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return int64(allocSample[0].Value.Uint64())
}

// guarded runs f on rt with the time and memory limits.
func (c *Codec) guarded(ctx context.Context, rt *goja.Runtime, f func() (goja.Value, error)) (v goja.Value, err error) {
	start := allocated()
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		deadline := time.NewTimer(c.lim.Timeout)
		defer deadline.Stop()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				rt.Interrupt(ErrCanceled)
				return
			case <-deadline.C:
				rt.Interrupt(ErrTimeout)
				return
			case <-tick.C:
				if allocated()-start > c.lim.MaxMemory {
					rt.Interrupt(ErrMemory)
					return
				}
			}
		}
	}()
	defer func() {
		close(done)
		wg.Wait()
		if p := recover(); p != nil {
			v, err = nil, fmt.Errorf("the codec failed: %v", p)
		}
	}()
	v, err = f()
	if err != nil {
		var ie *goja.InterruptedError
		if errors.As(err, &ie) {
			if e, ok := ie.Value().(error); ok {
				return nil, e
			}
		}
		var ex *goja.Exception
		if errors.As(err, &ex) {
			return nil, fmt.Errorf("the codec threw: %s", ex.Value().String())
		}
		var so *goja.StackOverflowError
		if errors.As(err, &so) {
			return nil, errors.New("the codec recursed deeper than its limit")
		}
		return nil, err
	}
	return v, nil
}

// call runs the named function with one argument built by in, and exports
// the result as plain Go values.
func (c *Codec) call(ctx context.Context, name string, in func(rt *goja.Runtime) goja.Value) (any, error) {
	rt, err := c.get()
	if err != nil {
		return nil, err
	}
	fn, ok := goja.AssertFunction(rt.Get(name))
	if !ok {
		c.put(rt)
		return nil, fmt.Errorf("the codec has no %s function", name)
	}
	v, err := c.guarded(ctx, rt, func() (goja.Value, error) { return fn(goja.Undefined(), in(rt)) })
	if err != nil {
		// A runtime that threw or was interrupted may hold broken state.
		return nil, err
	}
	out := v.Export()
	c.put(rt)
	return out, nil
}

// Uplink is decodeUplink's input.
type Uplink struct {
	Bytes    []byte
	FPort    int // 0 leaves fPort out
	RecvTime time.Time
	// Variables are the integrator's constants (keys, calibration).
	Variables map[string]any
	// Extra fields of the input object (source address, transport, the
	// device identified so far).
	Extra map[string]any
}

// Decoded is decodeUplink's result.
type Decoded struct {
	Data     map[string]any
	Warnings []string
	// DeviceID is the device the payload identifies (IMEI, serial), when
	// the codec found one.
	DeviceID string
	// Events are alarms and other occurrences, each an object with at
	// least a "type".
	Events []map[string]any
	// Units maps data keys to units.
	Units map[string]string
	// Reply is bytes to send straight back (a login acknowledgement).
	Reply []byte
	// Time is the acquisition time the payload carries, if any.
	Time time.Time
}

// DecoderError is a decodeUplink result with a non-empty errors list.
type DecoderError struct{ Errors []string }

func (e *DecoderError) Error() string {
	return "the codec reported: " + strings.Join(e.Errors, "; ")
}

func byteArray(rt *goja.Runtime, b []byte) goja.Value {
	vals := make([]any, len(b))
	for i, x := range b {
		vals[i] = int64(x)
	}
	return rt.NewArray(vals...)
}

// Decode runs decodeUplink.
func (c *Codec) Decode(ctx context.Context, u Uplink) (*Decoded, error) {
	if !c.hasDecode {
		return nil, ErrNoDecoder
	}
	if u.RecvTime.IsZero() {
		u.RecvTime = time.Now()
	}
	if c.proc != nil {
		return c.proc.decode(ctx, u)
	}
	out, err := c.call(ctx, "decodeUplink", func(rt *goja.Runtime) goja.Value {
		o := rt.NewObject()
		for k, v := range u.Extra {
			_ = o.Set(k, v)
		}
		_ = o.Set("bytes", byteArray(rt, u.Bytes))
		if u.FPort != 0 {
			_ = o.Set("fPort", u.FPort)
		}
		_ = o.Set("recvTime", recvTimeString(u.RecvTime))
		vars := map[string]any{}
		for k, v := range u.Variables {
			vars[k] = v
		}
		_ = o.Set("variables", vars)
		return o
	})
	if err != nil {
		return nil, err
	}
	return decodedResult("decodeUplink", out)
}

// recvTimeString formats the receive time in UTC with 0, 3, 6 or 9
// fractional digits, the way The Things Stack writes times in its JSON
// (protobuf JSON timestamps), so codecs that pass recvTime through give
// the same strings there and here. A plain RFC3339Nano would drop trailing
// zeros (.508306410 became .50830641).
func recvTimeString(t time.Time) string {
	t = t.UTC()
	s := t.Format("2006-01-02T15:04:05")
	switch ns := t.Nanosecond(); {
	case ns == 0:
	case ns%1_000_000 == 0:
		s += fmt.Sprintf(".%03d", ns/1_000_000)
	case ns%1_000 == 0:
		s += fmt.Sprintf(".%06d", ns/1_000)
	default:
		s += fmt.Sprintf(".%09d", ns)
	}
	return s + "Z"
}

// decodedResult checks and converts what decodeUplink or decodeDownlink
// (fn) returned.
func decodedResult(fn string, out any) (*Decoded, error) {
	m, ok := out.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must return an object such as {data: {...}}", fn)
	}
	if errs := strList(m["errors"]); len(errs) > 0 {
		return nil, &DecoderError{Errors: errs}
	}
	// Output variants that still carry usable data are kept; what was
	// changed is added to Warnings, which the IPE shows.
	d := &Decoded{Warnings: strList(m["warnings"])}
	note := func(format string, a ...any) { d.Warnings = append(d.Warnings, fmt.Sprintf(format, a...)) }
	if data, ok := m["data"].(map[string]any); ok {
		d.Data = dataObject(fn, data, note)
	} else if m["data"] != nil {
		d.Data = map[string]any{"value": plain(m["data"], 0)}
		note("%s's data is not an object; it was kept as data.value", fn)
	}
	if id, ok := m["deviceId"]; ok && id != nil {
		d.DeviceID = fmt.Sprint(id)
	}
	if evs, ok := m["events"].([]any); ok {
		for _, e := range evs {
			em, ok := e.(map[string]any)
			if !ok {
				em = map[string]any{"type": "event", "value": e}
				note("an event is not an object; it was kept as {type: event, value}")
			}
			d.Events = append(d.Events, plain(em, 0).(map[string]any))
		}
	}
	if us, ok := m["units"].(map[string]any); ok {
		d.Units = map[string]string{}
		for k, v := range us {
			d.Units[k] = fmt.Sprint(v)
		}
	}
	if r, ok := m["reply"]; ok && r != nil {
		if b, err := toBytes(r); err != nil {
			note("decodeUplink's reply is not sent: %v", err)
		} else {
			d.Reply = b
		}
	}
	if ts, ok := m["time"].(string); ok && ts != "" {
		if t, err := time.Parse(time.RFC3339Nano, ts); err != nil {
			note("%s's time %q is not an RFC 3339 time; the receive time is used", fn, ts)
		} else {
			d.Time = t
		}
	}
	return d, nil
}

// dataObject bounds and converts one data object a codec returned.
func dataObject(fn string, data map[string]any, note func(string, ...any)) map[string]any {
	if len(data) > maxDataKeys {
		keys := make([]string, 0, len(data))
		for k := range data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys[maxDataKeys:] {
			delete(data, k)
		}
		note("%s returned %d data fields; the first %d by name were kept", fn, len(keys), maxDataKeys)
	}
	return plain(data, 0).(map[string]any)
}

// DownlinkFrame is decodeDownlink's input: the bytes of a downlink, for
// showing what a queued or sent command means.
type DownlinkFrame struct {
	Bytes     []byte
	FPort     int // 0 leaves fPort out
	Variables map[string]any
	Extra     map[string]any
}

// DecodeDownlink runs decodeDownlink. The result has the shape of Decode's;
// errors in the result end the call with a *DecoderError.
func (c *Codec) DecodeDownlink(ctx context.Context, f DownlinkFrame) (*Decoded, error) {
	if !c.hasDecodeDown {
		return nil, ErrNoDownlinkDecoder
	}
	if c.proc != nil {
		return c.proc.decodeDownlink(ctx, f)
	}
	out, err := c.call(ctx, "decodeDownlink", func(rt *goja.Runtime) goja.Value {
		o := rt.NewObject()
		for k, v := range f.Extra {
			_ = o.Set(k, v)
		}
		_ = o.Set("bytes", byteArray(rt, f.Bytes))
		if f.FPort != 0 {
			_ = o.Set("fPort", f.FPort)
		}
		vars := map[string]any{}
		for k, v := range f.Variables {
			vars[k] = v
		}
		_ = o.Set("variables", vars)
		return o
	})
	if err != nil {
		return nil, err
	}
	return decodedResult("decodeDownlink", out)
}

// Normalized is normalizeUplink's result: one or more measurement objects
// (a single object returned by the script becomes a list of one).
type Normalized struct {
	Data     []map[string]any
	Warnings []string
}

// Normalize runs normalizeUplink on data decoded by decodeUplink. Errors in
// the result end the call with a *DecoderError.
func (c *Codec) Normalize(ctx context.Context, data map[string]any) (*Normalized, error) {
	if !c.hasNormalize {
		return nil, ErrNoNormalizer
	}
	if c.proc != nil {
		return c.proc.normalize(ctx, data)
	}
	out, err := c.call(ctx, "normalizeUplink", func(rt *goja.Runtime) goja.Value {
		o := rt.NewObject()
		_ = o.Set("data", jsValue(rt, data))
		return o
	})
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		return nil, errors.New("normalizeUplink must return an object such as {data: {...}}")
	}
	if errs := strList(m["errors"]); len(errs) > 0 {
		return nil, &DecoderError{Errors: errs}
	}
	n := &Normalized{Warnings: strList(m["warnings"])}
	note := func(format string, a ...any) { n.Warnings = append(n.Warnings, fmt.Sprintf(format, a...)) }
	var items []any
	switch x := m["data"].(type) {
	case nil:
	case []any:
		items = x
	default:
		items = []any{x}
	}
	if len(items) > maxDataKeys {
		note("normalizeUplink returned %d measurements; the first %d were kept", len(items), maxDataKeys)
		items = items[:maxDataKeys]
	}
	for i, e := range items {
		o, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("normalizeUplink's data element %d is not an object", i)
		}
		n.Data = append(n.Data, dataObject("normalizeUplink", o, note))
	}
	return n, nil
}

// jsValue converts a plain Go value (maps, slices, numbers, strings) into a
// JavaScript value of the runtime, so the script gets ordinary objects and
// arrays rather than wrapped Go values.
func jsValue(rt *goja.Runtime, v any) goja.Value {
	switch x := v.(type) {
	case map[string]any:
		o := rt.NewObject()
		for k, e := range x {
			_ = o.Set(k, jsValue(rt, e))
		}
		return o
	case []any:
		vals := make([]any, len(x))
		for i, e := range x {
			vals[i] = jsValue(rt, e)
		}
		return rt.NewArray(vals...)
	}
	return rt.ToValue(v)
}

// Downlink is encodeDownlink's input.
type Downlink struct {
	Data      any
	FPort     int
	Variables map[string]any
	Extra     map[string]any
}

// Encoded is encodeDownlink's result.
type Encoded struct {
	Bytes    []byte
	FPort    int
	Warnings []string
}

// Encode runs encodeDownlink.
func (c *Codec) Encode(ctx context.Context, d Downlink) (*Encoded, error) {
	if !c.hasEncode {
		return nil, ErrNoEncoder
	}
	if c.proc != nil {
		return c.proc.encode(ctx, d)
	}
	out, err := c.call(ctx, "encodeDownlink", func(rt *goja.Runtime) goja.Value {
		o := rt.NewObject()
		for k, v := range d.Extra {
			_ = o.Set(k, v)
		}
		_ = o.Set("data", d.Data)
		if d.FPort != 0 {
			_ = o.Set("fPort", d.FPort)
		}
		vars := map[string]any{}
		for k, v := range d.Variables {
			vars[k] = v
		}
		_ = o.Set("variables", vars)
		return o
	})
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		return nil, errors.New("encodeDownlink must return an object such as {bytes: [...]}")
	}
	if errs := strList(m["errors"]); len(errs) > 0 {
		return nil, &DecoderError{Errors: errs}
	}
	b, err := toBytes(m["bytes"])
	if err != nil {
		return nil, fmt.Errorf("encodeDownlink's bytes: %w", err)
	}
	e := &Encoded{Bytes: b, Warnings: strList(m["warnings"])}
	if p, ok := num(m["fPort"]); ok {
		e.FPort = int(p)
	}
	return e, nil
}

// FrameLength runs frameLength on the buffered bytes: n > 0 is the length
// of the frame at the start, 0 means more bytes are needed and -1 that the
// first byte does not start a frame.
func (c *Codec) FrameLength(ctx context.Context, buf []byte) (int, error) {
	if c.proc != nil {
		return c.proc.frameLength(ctx, buf)
	}
	out, err := c.call(ctx, "frameLength", func(rt *goja.Runtime) goja.Value { return byteArray(rt, buf) })
	if err != nil {
		return 0, err
	}
	n, ok := num(out)
	if !ok || n != math.Trunc(n) || n < -1 {
		return 0, fmt.Errorf("frameLength returned %v; it must be an integer of -1 or more", out)
	}
	return int(n), nil
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case int:
		return float64(x), true
	}
	return 0, false
}

func toBytes(v any) ([]byte, error) {
	switch x := v.(type) {
	case nil:
		return nil, errors.New("missing; an array of numbers 0..255 is expected")
	case []byte:
		if len(x) > maxOutBytes {
			return nil, fmt.Errorf("more than %d bytes", maxOutBytes)
		}
		return append([]byte(nil), x...), nil
	case goja.ArrayBuffer:
		return toBytes(x.Bytes())
	case []any:
		if len(x) > maxOutBytes {
			return nil, fmt.Errorf("more than %d bytes", maxOutBytes)
		}
		out := make([]byte, len(x))
		for i, e := range x {
			n, ok := num(e)
			if !ok || n < 0 || n > 255 || n != math.Trunc(n) {
				return nil, fmt.Errorf("element %d (%v) is not a byte", i, e)
			}
			out[i] = byte(n)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%T is not an array of bytes", v)
}

func strList(v any) []string {
	l, ok := v.([]any)
	if !ok {
		if s, ok := v.(string); ok && s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(l))
	for _, e := range l {
		out = append(out, fmt.Sprint(e))
	}
	return out
}

// plain turns exported values into JSON-safe ones: NaN and infinities
// become null, typed arrays become byte lists, functions are dropped.
func plain(v any, depth int) any {
	if depth > 32 {
		return nil
	}
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			if _, fn := e.(func(goja.FunctionCall) goja.Value); fn {
				continue
			}
			out[k] = plain(e, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = plain(e, depth+1)
		}
		return out
	case []byte:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = int64(e)
		}
		return out
	case goja.ArrayBuffer:
		return plain(x.Bytes(), depth)
	case string, bool, int64, nil, int, uint64:
		return x
	default:
		return fmt.Sprint(x)
	}
}
