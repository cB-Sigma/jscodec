package jscodec_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	codec "github.com/cB-Sigma/jscodec"
)

// The test binary doubles as the codec worker, as the sigma binary does.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == codec.WorkerCommand {
		os.Exit(codec.RunWorker())
	}
	os.Exit(m.Run())
}

func mustProc(t *testing.T, src string, lim codec.Limits) *codec.Codec {
	t.Helper()
	lim.Isolation = codec.Process
	c, err := codec.New("test.js", src, lim, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// Covers: SIG-IPE-819
func TestProcessParity(t *testing.T) {
	ctx := context.Background()
	in := mustNew(t, sample, codec.Limits{})
	pr := mustProc(t, sample, codec.Limits{})
	if in.HasDecoder() != pr.HasDecoder() || in.HasEncoder() != pr.HasEncoder() || in.HasFrameLength() != pr.HasFrameLength() {
		t.Fatal("the modes disagree on which functions the script has")
	}
	recv := time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC)
	ups := []codec.Uplink{
		{Bytes: []byte{0xFF, 0x38, 1}, RecvTime: recv, Variables: map[string]any{"imei": "356938035643809", "k": 3.5, "n": float64(7)},
			Extra: map[string]any{"source": "10.0.0.9:4000"}},
		{Bytes: []byte{0x01, 0x02, 0}, RecvTime: recv, FPort: 3},
		{Bytes: []byte{1}, RecvTime: recv}, // decoder error
	}
	for i, u := range ups {
		a, errA := in.Decode(ctx, u)
		b, errB := pr.Decode(ctx, u)
		if (errA == nil) != (errB == nil) || (errA != nil && errA.Error() != errB.Error()) {
			t.Fatalf("uplink %d: errors differ: %v / %v", i, errA, errB)
		}
		var de *codec.DecoderError
		if errA != nil && !errors.As(errB, &de) {
			t.Fatalf("uplink %d: process mode lost the decoder error type: %T", i, errB)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("uplink %d: results differ:\n%#v\n%#v", i, a, b)
		}
	}
	for _, d := range []codec.Downlink{{Data: map[string]any{"relay": true}}, {Data: map[string]any{"relay": 1.0}}} {
		a, errA := in.Encode(ctx, d)
		b, errB := pr.Encode(ctx, d)
		if (errA == nil) != (errB == nil) || !reflect.DeepEqual(a, b) {
			t.Fatalf("encode differs: %v %v / %v %v", a, errA, b, errB)
		}
	}
	for _, buf := range [][]byte{{0x7e}, {0x7e, 2, 1, 2}, {0x00}} {
		a, errA := in.FrameLength(ctx, buf)
		b, errB := pr.FrameLength(ctx, buf)
		if a != b || (errA == nil) != (errB == nil) {
			t.Fatalf("frameLength(% x) differs: %d %v / %d %v", buf, a, errA, b, errB)
		}
	}
	// The in-call limits hold in the worker too, with the same errors.
	lim := codec.Limits{Timeout: 150 * time.Millisecond}
	for _, body := range []string{"for (;;) {}", "new Array(1e9).fill(1);", "function f(n) { return f(n + 1) + 1; } f(0);", "require('fs');"} {
		src := "function decodeUplink(input) { " + body + " return {data: {}}; }"
		_, errA := mustNew(t, src, lim).Decode(ctx, codec.Uplink{Bytes: []byte{1}})
		_, errB := mustProc(t, src, lim).Decode(ctx, codec.Uplink{Bytes: []byte{1}})
		if errA == nil || errB == nil || errA.Error() != errB.Error() {
			t.Fatalf("%s: errors differ: %v / %v", body, errA, errB)
		}
		if errors.Is(errA, codec.ErrTimeout) != errors.Is(errB, codec.ErrTimeout) {
			t.Fatalf("%s: timeout not recognised in process mode: %v", body, errB)
		}
	}
	enc := mustProc(t, "function decodeUplink(i) { return {data: {}}; }", codec.Limits{})
	if _, err := enc.Encode(ctx, codec.Downlink{}); !errors.Is(err, codec.ErrNoEncoder) {
		t.Fatalf("got %v", err)
	}
	// Loading fails the same way.
	if _, err := codec.New("x.js", "function decodeUplink( {", codec.Limits{Isolation: codec.Process}, nil); err == nil || !strings.Contains(err.Error(), "does not compile") {
		t.Fatalf("got %v", err)
	}
	if _, err := codec.New("x.js", "for (;;) {} function decodeUplink() {}", codec.Limits{Timeout: 100 * time.Millisecond, Isolation: codec.Process}, nil); !errors.Is(err, codec.ErrTimeout) {
		t.Fatalf("got %v", err)
	}
	if _, err := codec.New("x.js", "var x = 1;", codec.Limits{Isolation: codec.Process}, nil); err == nil {
		t.Fatal("a script without codec functions was accepted")
	}
}

// Covers: SIG-IPE-820
func TestProcessHardLimits(t *testing.T) {
	ctx := context.Background()
	const guard = "if (input.bytes[0] === 2) return {data: {ok: true}}; "
	cases := []struct {
		name, body string
		lim        codec.Limits
		want       error
		within     time.Duration
	}{
		{"infinite loop", "for (;;) {}", codec.Limits{Timeout: 200 * time.Millisecond}, codec.ErrTimeout, 3 * time.Second},
		{"memory bomb", "var keep = []; for (;;) { var b = []; for (var i = 0; i < 20000; i++) b.push({i: i, s: 'v' + i}); keep.push(b); }",
			codec.Limits{Timeout: 20 * time.Second, MaxMemory: 8 << 20, ProcessMemory: 96 << 20}, codec.ErrMemory, 15 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.lim.Workers = 1
			c := mustProc(t, "function decodeUplink(input) { "+guard+tc.body+" }", codec.HardOnly(tc.lim))
			before := codec.WorkerPIDs(c)
			start := time.Now()
			_, err := c.Decode(ctx, codec.Uplink{Bytes: []byte{1}})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), "worker") {
				t.Fatalf("the error does not say the worker was stopped: %v", err)
			}
			if took := time.Since(start); took > tc.within {
				t.Fatalf("stopping took %s", took)
			}
			t.Logf("stopped after %s: %v", time.Since(start), err)
			deadline := time.Now().Add(2 * time.Second)
			for alive(before[0]) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if alive(before[0]) {
				// Reaped processes answer kill(0) with ESRCH; a zombie would too
				// once waited for.
				t.Fatalf("worker %d still runs", before[0])
			}
			// The next call works, on a fresh worker.
			d, err := c.Decode(ctx, codec.Uplink{Bytes: []byte{2}})
			if err != nil || d.Data["ok"] != true {
				t.Fatalf("the codec is unusable after the kill: %v %v", d, err)
			}
			after := codec.WorkerPIDs(c)
			if len(after) != 1 || after[0] == before[0] {
				t.Fatalf("workers before %v, after %v", before, after)
			}
		})
	}
}

// Covers: SIG-IPE-821
func TestProcessCrashRecovery(t *testing.T) {
	ctx := context.Background()
	c := mustProc(t, sample, codec.Limits{Workers: 1})
	pids := codec.WorkerPIDs(c)
	if len(pids) != 1 {
		t.Fatalf("workers %v", pids)
	}
	// A worker killed from outside while idle is replaced.
	if err := syscall.Kill(pids[0], syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	d, err := c.Decode(ctx, codec.Uplink{Bytes: []byte{0, 5, 0}})
	if err != nil || n(d.Data["temperature"]) != 0.5 {
		t.Fatalf("after an idle kill: %v %v", d, err)
	}
	// A worker that dies during a call fails that call only.
	if err := codec.CrashWorker(ctx, c); !errors.Is(err, codec.ErrWorkerCrashed) {
		t.Fatalf("got %v", err)
	}
	d, err = c.Decode(ctx, codec.Uplink{Bytes: []byte{0, 6, 0}})
	if err != nil || n(d.Data["temperature"]) != 0.6 {
		t.Fatalf("after a crash: %v %v", d, err)
	}
	// A canceled call stops its worker and the next call still works.
	loop := mustProc(t, "function decodeUplink(input) { if (input.bytes[0] === 2) return {data: {}}; for (;;) {} }",
		codec.Limits{Timeout: 10 * time.Second, Workers: 1})
	cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := loop.Decode(cctx, codec.Uplink{Bytes: []byte{1}}); !errors.Is(err, codec.ErrCanceled) {
		t.Fatalf("got %v", err)
	}
	if _, err := loop.Decode(ctx, codec.Uplink{Bytes: []byte{2}}); err != nil {
		t.Fatal(err)
	}
}

// Covers: SIG-IPE-822
func TestProcessPool(t *testing.T) {
	ctx := context.Background()
	c := mustProc(t, sample+"\nfunction encodeSlow() {}\n", codec.Limits{Workers: 3})
	var wg sync.WaitGroup
	errs := make(chan error, 48)
	for i := 0; i < 48; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d, err := c.Decode(ctx, codec.Uplink{Bytes: []byte{0, byte(i), 0}})
			if err == nil && n(d.Data["temperature"]) != float64(i)/10 {
				err = errors.New("wrong value")
			}
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	pids := codec.WorkerPIDs(c)
	if len(pids) == 0 || len(pids) > 3 {
		t.Fatalf("workers %v; the pool holds at most 3", pids)
	}
	// A call stuck in one worker does not hold up the others. The busy
	// loop allocates on every Date.now(), and MaxMemory counts bytes
	// allocated during the call, so the slow call gets room for 800ms of it.
	slow := mustProc(t, "function decodeUplink(input) { if (input.bytes[0] === 1) { var t = Date.now(); while (Date.now() - t < 800) {} } return {data: {b: input.bytes[0]}}; }",
		codec.Limits{Timeout: 2 * time.Second, Workers: 2, MaxMemory: 1 << 30})
	done := make(chan error, 1)
	go func() { _, err := slow.Decode(ctx, codec.Uplink{Bytes: []byte{1}}); done <- err }()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	if _, err := slow.Decode(ctx, codec.Uplink{Bytes: []byte{2}}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 600*time.Millisecond {
		t.Fatalf("a quick call waited %s behind a slow one", took)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Close stops every worker; later calls fail.
	all := codec.WorkerPIDs(slow)
	_ = slow.Close()
	if _, err := slow.Decode(ctx, codec.Uplink{Bytes: []byte{2}}); !errors.Is(err, codec.ErrClosed) {
		t.Fatalf("got %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for slices.ContainsFunc(all, alive) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if slices.ContainsFunc(all, alive) {
		t.Fatalf("workers %v outlived Close", all)
	}
}

// Covers: SIG-IPE-823
func TestIsolationSetting(t *testing.T) {
	if _, err := codec.New("x.js", sample, codec.Limits{Isolation: "vm"}, nil); err == nil || !strings.Contains(err.Error(), "isolation") {
		t.Fatalf("got %v", err)
	}
	l := codec.Sandbox{Isolation: codec.Process, Workers: 4, ProcessMemory: 1 << 27}.Apply(codec.Limits{Timeout: time.Second})
	if l.Isolation != codec.Process || l.Workers != 4 || l.ProcessMemory != 1<<27 || l.Timeout != time.Second {
		t.Fatalf("Apply: %+v", l)
	}
	// The worker gets no environment: the server's secrets stay out of it.
	t.Setenv("SIGMA_TEST_SECRET", "s3cret")
	c := mustProc(t, "function decodeUplink(input) { return {data: {}}; }", codec.Limits{Workers: 1})
	pid := codec.WorkerPIDs(c)[0]
	env, ok := processEnv(pid)
	if !ok || !strings.Contains(env, "GOMAXPROCS=2") {
		t.Skipf("cannot read the worker's environment here (%q)", env)
	}
	if strings.Contains(env, "s3cret") {
		t.Fatal("the worker inherited the environment")
	}
}
