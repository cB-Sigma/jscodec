// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package devicerepo_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/cB-Sigma/jscodec"
	"github.com/cB-Sigma/jscodec/devicerepo"
)

// A made-up repository in the lorawan-devices layout.
var demo = fstest.MapFS{
	"vendor/index.yaml":              {Data: []byte("vendors:\n  - id: acme\n    name: Acme\n    vendorID: 99\n")},
	"vendor/acme/index.yaml":         {Data: []byte("endDevices:\n  - thermo\n")},
	"vendor/acme/thermo.yaml":        {Data: []byte("name: Thermo\nfirmwareVersions:\n  - version: '1.0'\n    profiles:\n      EU863-870:\n        id: p\n        codec: thermo-codec\n  - version: '2.0'\n    profiles:\n      US902-928:\n        id: p2\n        vendorID: module-maker\n        codec: thermo2-codec\n")},
	"vendor/acme/thermo-codec.yaml":  {Data: []byte("uplinkDecoder:\n  fileName: thermo.js\n  examples:\n    - description: t\n      input:\n        bytes: [0, 235]\n        fPort: 1\n      output:\n        data:\n          t: 23.5\n")},
	"vendor/acme/thermo2-codec.yaml": {Data: []byte("uplinkDecoder:\n  fileName: ../other/evil.js\n")},
	"vendor/acme/thermo.js":          {Data: []byte("function decodeUplink(i) { return { data: { t: (i.bytes[0] << 8 | i.bytes[1]) / 10 } }; }")},
}

// Lookup resolves device, firmware and region to the codec, whose script
// runs and reproduces its example; unknown names and scripts outside the
// vendor's folder are refused.
// Covers: SIG-IPE-6744
func TestLookup(t *testing.T) {
	r, err := devicerepo.Open(demo)
	if err != nil {
		t.Fatal(err)
	}
	vs, _ := r.Vendors()
	ds, _ := r.Devices("acme")
	if len(vs) != 1 || vs[0].VendorID != 99 || len(ds) != 1 {
		t.Fatalf("vendors %v devices %v", vs, ds)
	}
	c, err := r.Lookup("acme", "thermo", "", "")
	if err != nil || c.ID != "thermo-codec" || c.UplinkDecoder == nil || len(c.UplinkDecoder.Examples) != 1 {
		t.Fatalf("lookup %+v %v", c, err)
	}
	scripts, err := r.Scripts(c)
	if err != nil {
		t.Fatal(err)
	}
	k, err := jscodec.New("thermo.js", scripts["thermo.js"], jscodec.Limits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := k.Decode(context.Background(), jscodec.Uplink{Bytes: []byte{0, 235}, FPort: 1})
	if err != nil || d.Data["t"] != 23.5 {
		t.Errorf("decode %+v %v", d, err)
	}
	if _, err := r.Lookup("acme", "thermo", "1.0", "AS923"); !errors.Is(err, devicerepo.ErrNotFound) {
		t.Errorf("unknown region: %v", err)
	}
	if _, err := r.Lookup("acme", "nothing", "", ""); !errors.Is(err, devicerepo.ErrNotFound) {
		t.Errorf("unknown device: %v", err)
	}
	c2, err := r.Lookup("acme", "thermo", "2.0", "US902-928")
	if err != nil || c2.ID != "thermo2-codec" || c2.Vendor != "acme" {
		t.Fatalf("profile of another vendor: %+v %v", c2, err)
	}
	if _, err := r.Scripts(c2); err == nil {
		t.Error("a script outside the vendor's folder was read")
	}
	if _, err := r.Devices("../x"); err == nil {
		t.Error("a path was taken as a vendor")
	}
}
