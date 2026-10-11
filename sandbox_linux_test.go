// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package jscodec_test

import (
	"context"
	"strings"
	"testing"

	"github.com/cB-Sigma/jscodec"
)

// With Seccomp a worker cannot open sockets or files; with Namespaces it
// sees no network but loopback; without either it can do both. Workers
// report their peak resident size.
// Covers: SIG-IPE-6743
func TestLinuxWorkerSandbox(t *testing.T) {
	ctx := context.Background()
	src := `function decodeUplink(i) { return { data: { n: i.bytes.length } }; }`
	open, err := jscodec.New("t.js", src, jscodec.Limits{Isolation: jscodec.Process, Workers: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	if n, err := jscodec.SandboxProbe(ctx, open); err != nil || n&3 != 0 {
		t.Errorf("unsandboxed probe %b %v", n, err)
	}
	if _, err := open.Decode(ctx, jscodec.Uplink{Bytes: []byte{1}}); err != nil {
		t.Fatal(err)
	}
	if u := open.Usage(); u.Calls == 0 || u.PeakRSS <= 0 {
		t.Errorf("usage %+v", u)
	}

	sc, err := jscodec.New("t.js", src, jscodec.Limits{Isolation: jscodec.Process, Workers: 1, Seccomp: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	if d, err := sc.Decode(ctx, jscodec.Uplink{Bytes: []byte{1, 2}}); err != nil || d.Data["n"] != int64(2) {
		t.Fatalf("decode under seccomp: %+v %v", d, err)
	}
	if n, err := jscodec.SandboxProbe(ctx, sc); err != nil || n&3 != 3 {
		t.Errorf("seccomp probe %b %v (want socket and open refused)", n, err)
	}

	ns, err := jscodec.New("t.js", src, jscodec.Limits{Isolation: jscodec.Process, Workers: 1, Namespaces: true}, nil)
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") || strings.Contains(err.Error(), "invalid argument") {
			t.Skipf("user namespaces are not allowed here: %v", err)
		}
		t.Fatal(err)
	}
	defer ns.Close()
	if n, err := jscodec.SandboxProbe(ctx, ns); err != nil || n&4 == 0 {
		t.Errorf("namespace probe %b %v (want no network but loopback)", n, err)
	}
	if d, err := ns.Decode(ctx, jscodec.Uplink{Bytes: []byte{1}}); err != nil || d.Data["n"] != int64(1) {
		t.Errorf("decode in namespaces: %+v %v", d, err)
	}
}
