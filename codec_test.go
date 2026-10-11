package jscodec_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	codec "github.com/cB-Sigma/jscodec"
)

const sample = `
var calls = 0;
function decodeUplink(input) {
  calls++;
  var b = input.bytes;
  if (b.length < 3) return { errors: ["short frame"] };
  var t = ((b[0] << 8) | b[1]); if (t & 0x8000) t -= 0x10000;
  var out = { data: { temperature: t / 10, door: b[2] === 1 }, units: { temperature: "Cel" } };
  if (input.variables.imei) out.deviceId = input.variables.imei;
  if (b[2] === 1) out.events = [{ type: "door-open", source: input.source }];
  out.reply = [0x78, 0x78, 0x01];
  var dv = new DataView(new ArrayBuffer(4)); dv.setFloat32(0, 1.5); out.data.f = dv.getFloat32(0);
  return out;
}
function encodeDownlink(input) {
  if (typeof input.data.relay !== "boolean") return { errors: ["relay must be true or false"] };
  return { bytes: [0xA0, input.data.relay ? 1 : 0], fPort: 7 };
}
function frameLength(b) { if (b.length < 2) return 0; if (b[0] !== 0x7e) return -1; return 2 + b[1]; }
`

func n(v any) float64 {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case float64:
		return x
	}
	return -1e9
}

func mustNew(t *testing.T, src string, lim codec.Limits) *codec.Codec {
	t.Helper()
	c, err := codec.New("test.js", src, lim, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Covers: SIG-IPE-240
func TestDecodeEncodeFrame(t *testing.T) {
	c := mustNew(t, sample, codec.Limits{})
	ctx := context.Background()
	d, err := c.Decode(ctx, codec.Uplink{Bytes: []byte{0xFF, 0x38, 1}, Variables: map[string]any{"imei": "356938035643809"},
		Extra: map[string]any{"source": "10.0.0.9:4000"}})
	if err != nil {
		t.Fatal(err)
	}
	if n(d.Data["temperature"]) != -20 || d.Data["door"] != true || n(d.Data["f"]) != 1.5 {
		t.Fatalf("data %v", d.Data)
	}
	if d.DeviceID != "356938035643809" || d.Units["temperature"] != "Cel" || len(d.Events) != 1 || d.Events[0]["source"] != "10.0.0.9:4000" {
		t.Fatalf("decoded %+v", d)
	}
	if string(d.Reply) != "\x78\x78\x01" {
		t.Fatalf("reply %x", d.Reply)
	}
	if _, err := c.Decode(ctx, codec.Uplink{Bytes: []byte{1}}); err == nil || !strings.Contains(err.Error(), "short frame") {
		t.Fatalf("decoder errors not reported: %v", err)
	}
	e, err := c.Encode(ctx, codec.Downlink{Data: map[string]any{"relay": true}})
	if err != nil || string(e.Bytes) != "\xA0\x01" || e.FPort != 7 {
		t.Fatalf("encode %+v %v", e, err)
	}
	var de *codec.DecoderError
	if _, err := c.Encode(ctx, codec.Downlink{Data: map[string]any{"relay": 3}}); !errors.As(err, &de) {
		t.Fatalf("encoder errors not reported: %v", err)
	}
	for _, tc := range []struct {
		in   []byte
		want int
	}{{[]byte{0x7e}, 0}, {[]byte{0x7e, 3, 1}, 5}, {[]byte{0x00, 1}, -1}} {
		if n, err := c.FrameLength(ctx, tc.in); err != nil || n != tc.want {
			t.Fatalf("frameLength(%x) = %d %v, want %d", tc.in, n, err, tc.want)
		}
	}
	if _, err := codec.New("x.js", "var x = 1;", codec.Limits{}, nil); err == nil {
		t.Fatal("a script without codec functions was accepted")
	}
	if _, err := codec.New("x.js", "function decodeUplink( {", codec.Limits{}, nil); err == nil {
		t.Fatal("a script that does not compile was accepted")
	}
}

// Covers: SIG-IPE-241
func TestSandboxLimits(t *testing.T) {
	lim := codec.Limits{Timeout: 150 * time.Millisecond, MaxMemory: 16 << 20}
	ctx := context.Background()
	cases := []struct {
		name, body string
		want       error
		contains   string
	}{
		{"infinite loop", "for (;;) {}", codec.ErrTimeout, ""},
		{"busy callback loop", "var a = [1,2,3]; for (;;) { a.forEach(function (x) { return x; }); }", nil, ""},
		{"string doubling", "var s = 'x'; for (;;) { s = s + s; }", nil, ""},
		{"array growth", "var a = []; for (;;) { a = a.concat([{k: 'vvvvvvvvvvvvvvvvvvvv'}]); if (a.length > 60000) a = []; var b = []; for (var i = 0; i < 1000; i++) b.push({i: i}); }", nil, ""},
		{"huge sparse fill", "var a = []; a.length = 1e9; a.fill(0);", nil, "array longer"},
		{"huge join", "var a = []; a[1e8] = 1; a.join(',');", nil, "array longer"},
		{"array constructor", "new Array(1e9).fill(1);", nil, "array longer"},
		{"array-like call", "Array.prototype.map.call({length: 1e9}, function (x) { return x; });", nil, "array longer"},
		{"huge repeat", "'abc'.repeat(1e9);", nil, "string longer"},
		{"huge pad", "''.padStart(1e9, 'x');", nil, "string longer"},
		{"huge buffer", "new ArrayBuffer(1e9);", nil, "buffer larger"},
		{"huge typed array", "new Float64Array(1e8);", nil, "buffer larger"},
		{"huge json", "var a = []; a.length = 1e9; JSON.stringify(a);", nil, "array longer"},
		{"deep recursion", "function f(n) { return f(n + 1) + 1; } f(0);", nil, "recursed"},
		{"no require", "require('fs');", nil, "threw"},
		{"no fetch", "fetch('http://example.com');", nil, "threw"},
		{"catastrophic regexp", "/^(a|aa)+(?=b)c$/.test('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa!'); for (;;) {}", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := mustNew(t, "function decodeUplink(input) { "+tc.body+" return {data: {}}; }", lim)
			start := time.Now()
			_, err := c.Decode(ctx, codec.Uplink{Bytes: []byte{1}})
			took := time.Since(start)
			if err == nil {
				t.Fatal("the call was not stopped")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if tc.contains != "" && !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("got %v, want it to mention %q", err, tc.contains)
			}
			if took > 2*time.Second {
				t.Fatalf("stopping took %s", took)
			}
			// The codec still works after a killed call (a fresh runtime).
			c2 := mustNew(t, "function decodeUplink(input) { if (input.bytes[0] === 2) return {data: {ok: true}}; "+tc.body+" }", lim)
			_, _ = c2.Decode(ctx, codec.Uplink{Bytes: []byte{1}})
			d, err := c2.Decode(ctx, codec.Uplink{Bytes: []byte{2}})
			if err != nil || d.Data["ok"] != true {
				t.Fatalf("codec unusable after a killed call: %v %v", d, err)
			}
		})
	}
	t.Run("memory cap", func(t *testing.T) {
		c := mustNew(t, "function decodeUplink(input) { var keep = []; for (;;) { keep.push('x'.repeat(1000) + keep.length); if (keep.length > 60000) keep = [keep]; } }",
			codec.Limits{Timeout: 5 * time.Second, MaxMemory: 8 << 20})
		start := time.Now()
		_, err := c.Decode(ctx, codec.Uplink{Bytes: []byte{1}})
		if !errors.Is(err, codec.ErrMemory) {
			t.Fatalf("got %v, want the memory limit", err)
		}
		if time.Since(start) > 4*time.Second {
			t.Fatal("the memory limit was not what stopped the call")
		}
	})
	t.Run("canceled", func(t *testing.T) {
		c := mustNew(t, "function decodeUplink(input) { for (;;) {} }", codec.Limits{Timeout: 10 * time.Second})
		cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		if _, err := c.Decode(cctx, codec.Uplink{}); !errors.Is(err, codec.ErrCanceled) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("top-level loop", func(t *testing.T) {
		if _, err := codec.New("x.js", "for (;;) {} function decodeUplink() {}", lim, nil); !errors.Is(err, codec.ErrTimeout) {
			t.Fatalf("got %v", err)
		}
	})
}

// Covers: SIG-IPE-241
func TestConcurrentCalls(t *testing.T) {
	c := mustNew(t, sample, codec.Limits{})
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		go func(i int) {
			d, err := c.Decode(context.Background(), codec.Uplink{Bytes: []byte{0, byte(i), 0}})
			if err == nil && n(d.Data["temperature"]) != float64(i)/10 {
				err = errors.New("wrong value")
			}
			errs <- err
		}(i)
	}
	for i := 0; i < 32; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

// Covers: SIG-IPE-3543
func TestOutputVariantsKeptWithWarnings(t *testing.T) {
	c := mustNew(t, `function decodeUplink(input) {
  var data = {};
  for (var i = 0; i < 600; i++) { data["k" + (1000 + i)] = i; }
  if (input.bytes[0] === 1) return {data: 42, events: ["door"], reply: "not bytes", time: "yesterday"};
  return {data: data};
}`, codec.Limits{})
	ctx := context.Background()
	d, err := c.Decode(ctx, codec.Uplink{Bytes: []byte{1}})
	if err != nil {
		t.Fatalf("variants refused: %v", err)
	}
	if d.Data["value"] != int64(42) && d.Data["value"] != 42.0 {
		t.Errorf("data = %v", d.Data)
	}
	if len(d.Events) != 1 || d.Events[0]["value"] != "door" || d.Events[0]["type"] != "event" {
		t.Errorf("events = %v", d.Events)
	}
	if d.Reply != nil || !d.Time.IsZero() || len(d.Warnings) != 4 {
		t.Errorf("reply %x, time %v, warnings %q", d.Reply, d.Time, d.Warnings)
	}
	d, err = c.Decode(ctx, codec.Uplink{Bytes: []byte{2}})
	if err != nil || len(d.Data) != 512 || len(d.Warnings) != 1 {
		t.Fatalf("too many fields: %d fields, warnings %q, %v", len(d.Data), d.Warnings, err)
	}
}
