// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package jscodec_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	codec "github.com/cB-Sigma/jscodec"
)

const ttnStyle = `
function decodeUplink(input) {
  return { data: { t: input.bytes[0] / 2, at: input.recvTime } };
}
function decodeDownlink(input) {
  if (input.bytes.length === 0) return { errors: ["empty downlink"] };
  return { data: { command: input.bytes[0] === 1 ? "on" : "off", port: input.fPort }, warnings: ["looked at one byte"] };
}
function normalizeUplink(input) {
  if (input.data.t === undefined) return { errors: ["no temperature"] };
  if (input.data.two) return { data: [{ air: { temperature: input.data.t } }, { air: { temperature: input.data.t + 1 } }] };
  return { data: { air: { temperature: input.data.t, rh: input.data.list[1] } } };
}
`

func TestDecodeDownlinkAndNormalize(t *testing.T) {
	ctx := context.Background()
	for _, iso := range []codec.Isolation{codec.InProcess, codec.Process} {
		c, err := codec.New("ttn.js", ttnStyle, codec.Limits{Isolation: iso}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		if !c.HasDownlinkDecoder() || !c.HasNormalizer() || c.HasEncoder() {
			t.Fatalf("%s: functions reported wrongly", iso)
		}
		d, err := c.DecodeDownlink(ctx, codec.DownlinkFrame{Bytes: []byte{1}, FPort: 9})
		if err != nil {
			t.Fatalf("%s: %v", iso, err)
		}
		if d.Data["command"] != "on" || d.Data["port"] != int64(9) || !reflect.DeepEqual(d.Warnings, []string{"looked at one byte"}) {
			t.Fatalf("%s: decodeDownlink gave %#v", iso, d)
		}
		var de *codec.DecoderError
		if _, err := c.DecodeDownlink(ctx, codec.DownlinkFrame{}); !errors.As(err, &de) || de.Errors[0] != "empty downlink" {
			t.Fatalf("%s: got %v", iso, err)
		}
		n, err := c.Normalize(ctx, map[string]any{"t": 21.5, "list": []any{int64(1), 55.0}})
		if err != nil {
			t.Fatalf("%s: %v", iso, err)
		}
		want := []map[string]any{{"air": map[string]any{"temperature": 21.5, "rh": int64(55)}}}
		if !reflect.DeepEqual(n.Data, want) {
			t.Fatalf("%s: one object becomes a list of one: %#v", iso, n.Data)
		}
		n, err = c.Normalize(ctx, map[string]any{"t": 20.0, "two": true})
		if err != nil || len(n.Data) != 2 {
			t.Fatalf("%s: a list stays a list: %#v %v", iso, n, err)
		}
		if _, err := c.Normalize(ctx, map[string]any{}); !errors.As(err, &de) || de.Errors[0] != "no temperature" {
			t.Fatalf("%s: got %v", iso, err)
		}
	}
	// A script may hold only decodeDownlink; the others then say so.
	c := mustNew(t, "function decodeDownlink(i) { return { data: {} }; }", codec.Limits{})
	if _, err := c.Decode(ctx, codec.Uplink{}); !errors.Is(err, codec.ErrNoDecoder) {
		t.Fatalf("got %v", err)
	}
	if _, err := c.Normalize(ctx, nil); !errors.Is(err, codec.ErrNoNormalizer) {
		t.Fatalf("got %v", err)
	}
	c = mustNew(t, "function decodeUplink(i) { return { data: {} }; }", codec.Limits{})
	if _, err := c.DecodeDownlink(ctx, codec.DownlinkFrame{}); !errors.Is(err, codec.ErrNoDownlinkDecoder) {
		t.Fatalf("got %v", err)
	}
	p := mustProc(t, "function decodeUplink(i) { return { data: {} }; }", codec.Limits{})
	if _, err := p.Normalize(ctx, nil); !errors.Is(err, codec.ErrNoNormalizer) {
		t.Fatalf("process: got %v", err)
	}
}

// recvTime carries 0, 3, 6 or 9 fractional digits, so a time written as
// .508306410 reaches the script as written.
func TestRecvTimeDigits(t *testing.T) {
	ctx := context.Background()
	c := mustNew(t, ttnStyle, codec.Limits{})
	for ns, want := range map[int]string{
		0:           "2023-07-19T07:51:25Z",
		500_000_000: "2023-07-19T07:51:25.500Z",
		508_306_000: "2023-07-19T07:51:25.508306Z",
		508_306_410: "2023-07-19T07:51:25.508306410Z",
		598_957_793: "2023-07-19T07:51:25.598957793Z",
	} {
		at := time.Date(2023, 7, 19, 9, 51, 25, ns, time.FixedZone("CEST", 2*3600))
		d, err := c.Decode(ctx, codec.Uplink{Bytes: []byte{4}, RecvTime: at})
		if err != nil {
			t.Fatal(err)
		}
		if d.Data["at"] != want {
			t.Errorf("%d ns: recvTime %v, want %s", ns, d.Data["at"], want)
		}
	}
}
