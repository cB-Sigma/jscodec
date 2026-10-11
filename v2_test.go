// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package jscodec_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/cB-Sigma/jscodec"
)

func newV2(t *testing.T, src string, l jscodec.Limits) *jscodec.Codec {
	t.Helper()
	c, err := jscodec.New("t.js", src, l, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// The Node-style Buffer (with NodeBuffer): constructors, readers and
// writers in both byte orders, hex, base64 and UTF-8 strings, concat and
// slices; without NodeBuffer there is none.
// Covers: SIG-IPE-6740
func TestNodeBuffer(t *testing.T) {
	src := `function decodeUplink(input) {
  var b = Buffer.from(input.bytes);
  var w = Buffer.alloc(8);
  w.writeUInt16BE(0x1234, 0); w.writeInt32LE(-2, 2); w.writeUInt8(255, 6);
  var u = Buffer.from('héllo €', 'utf8');
  return { data: {
    u16be: b.readUInt16BE(0), u16le: b.readUInt16LE(0), i8: b.readInt8(2), i16be: b.readInt16BE(2),
    u32be: b.readUInt32BE(0), f32: Buffer.from('41bc0000', 'hex').readFloatBE(0),
    hex: b.toString('hex'), b64: b.toString('base64'), back: Buffer.from(b.toString('base64'), 'base64').toString('hex'),
    w: w.toString('hex'), utf8: u.toString(), utf8len: u.length, isBuf: Buffer.isBuffer(b), isU8: b instanceof Uint8Array,
    cat: Buffer.concat([b.slice(0, 1), Buffer.from([9])]).toString('hex'), uintbe: b.readUIntBE(0, 3), intle: b.readIntLE(1, 2)
  } };
}`
	c := newV2(t, src, jscodec.Limits{NodeBuffer: true})
	d, err := c.Decode(context.Background(), jscodec.Uplink{Bytes: []byte{0x01, 0x02, 0xFE, 0x10}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"u16be": int64(0x0102), "u16le": int64(0x0201), "i8": int64(-2), "i16be": int64(-496), "u32be": int64(0x0102FE10),
		"f32": 23.5, "hex": "0102fe10", "b64": "AQL+EA==", "back": "0102fe10", "w": "1234feffffffff00",
		"utf8": "héllo €", "utf8len": int64(10), "isBuf": true, "isU8": true, "cat": "0109", "uintbe": int64(0x0102FE), "intle": int64(-510),
	}
	for k, v := range want {
		if g := d.Data[k]; !reflect.DeepEqual(g, v) {
			t.Errorf("%s = %#v, want %#v", k, g, v)
		}
	}
	plain := newV2(t, `function decodeUplink(i) { return { data: { has: typeof Buffer !== 'undefined' } }; }`, jscodec.Limits{})
	if d, _ := plain.Decode(context.Background(), jscodec.Uplink{}); d.Data["has"] != false {
		t.Error("Buffer exists without NodeBuffer")
	}
}

// The Things Network v2 (Decoder, Converter, Validator, Encoder) and
// ChirpStack v3 (Decode, Encode) entry points run as decodeUplink and
// encodeDownlink; a script's own decodeUplink wins.
// Covers: SIG-IPE-6741
func TestLegacyEntryPoints(t *testing.T) {
	ctx := context.Background()
	v2 := newV2(t, `function Decoder(bytes, port) { return { t: bytes[0], port: port }; }
function Converter(d, port) { d.c = true; return d; }
function Validator(d, port) { return d.t < 100; }
function Encoder(obj, port) { return [obj.on ? 1 : 0, port]; }`, jscodec.Limits{})
	d, err := v2.Decode(ctx, jscodec.Uplink{Bytes: []byte{21}, FPort: 3})
	if err != nil || d.Data["t"] != int64(21) || d.Data["port"] != int64(3) || d.Data["c"] != true {
		t.Errorf("v2 decode %+v %v", d, err)
	}
	var de *jscodec.DecoderError
	if _, err := v2.Decode(ctx, jscodec.Uplink{Bytes: []byte{200}, FPort: 3}); !errors.As(err, &de) {
		t.Errorf("v2 validator: %v", err)
	}
	e, err := v2.Encode(ctx, jscodec.Downlink{Data: map[string]any{"on": true}, FPort: 7})
	if err != nil || !reflect.DeepEqual(e.Bytes, []byte{1, 7}) || e.FPort != 7 {
		t.Errorf("v2 encode %+v %v", e, err)
	}
	cs := newV2(t, `function Decode(fPort, bytes, variables) { return { v: bytes[0] * variables.k, p: fPort }; }
function Encode(fPort, obj, variables) { return [obj.x, fPort]; }`, jscodec.Limits{})
	d, err = cs.Decode(ctx, jscodec.Uplink{Bytes: []byte{4}, FPort: 2, Variables: map[string]any{"k": 3}})
	if err != nil || d.Data["v"] != int64(12) || d.Data["p"] != int64(2) {
		t.Errorf("chirpstack decode %+v %v", d, err)
	}
	if e, err := cs.Encode(ctx, jscodec.Downlink{Data: map[string]any{"x": 5}, FPort: 9}); err != nil || !reflect.DeepEqual(e.Bytes, []byte{5, 9}) {
		t.Errorf("chirpstack encode %+v %v", e, err)
	}
	own := newV2(t, `function decodeUplink(i) { return { data: { own: true } }; } function Decoder() { return { own: false }; }`, jscodec.Limits{})
	if d, _ := own.Decode(ctx, jscodec.Uplink{}); d.Data["own"] != true {
		t.Error("the legacy Decoder replaced decodeUplink")
	}
}

// Each call runs in a fresh runtime unless KeepState; the whole result
// (data with errors, other keys, empty lists) is in Result; OmitRecvTime
// leaves recvTime out.
// Covers: SIG-IPE-6742
func TestFreshStateResultAndRecvTime(t *testing.T) {
	ctx := context.Background()
	src := `var n = 0;
function decodeUplink(i) { n++; return { data: { n: n, rt: typeof i.recvTime }, warnings: [], extra: 'x' }; }
function decodeDownlink(i) { return { data: { partial: 1 }, warnings: ['w'], errors: ['bad'] }; }`
	for _, iso := range []jscodec.Isolation{jscodec.InProcess, jscodec.Process} {
		if iso == jscodec.Process && testing.Short() {
			continue
		}
		fresh := newV2(t, src, jscodec.Limits{Isolation: iso, Workers: 1})
		keep := newV2(t, src, jscodec.Limits{Isolation: iso, Workers: 1, KeepState: true})
		for range 2 {
			_, _ = fresh.Decode(ctx, jscodec.Uplink{})
			_, _ = keep.Decode(ctx, jscodec.Uplink{})
		}
		f, _ := fresh.Decode(ctx, jscodec.Uplink{OmitRecvTime: true})
		k, _ := keep.Decode(ctx, jscodec.Uplink{})
		if f.Data["n"] != int64(1) || k.Data["n"] != int64(3) {
			t.Errorf("%s: fresh n %v, kept n %v", iso, f.Data["n"], k.Data["n"])
		}
		if f.Data["rt"] != "undefined" || k.Data["rt"] != "string" {
			t.Errorf("%s: recvTime %v / %v", iso, f.Data["rt"], k.Data["rt"])
		}
		if w, ok := f.Result["warnings"].([]any); !ok || len(w) != 0 || f.Result["extra"] != "x" {
			t.Errorf("%s: result %#v", iso, f.Result)
		}
		_, err := fresh.DecodeDownlink(ctx, jscodec.DownlinkFrame{Bytes: []byte{1}})
		var de *jscodec.DecoderError
		if !errors.As(err, &de) || !reflect.DeepEqual(de.Result["data"], map[string]any{"partial": int64(1)}) || len(de.Errors) != 1 {
			t.Errorf("%s: data with errors %#v %v", iso, de, err)
		}
	}
}
