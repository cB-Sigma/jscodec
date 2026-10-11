// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package ttn

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cB-Sigma/jscodec/devicerepo"
)

// TestDeviceRepoLookup resolves every device of every vendor of a full
// lorawan-devices checkout (SIGMA_JSCODEC_TTN=<dir>; the fetched subset has
// no device files) to its codec and reads the codec's scripts.
// Covers: SIG-IPE-6744
func TestDeviceRepoLookup(t *testing.T) {
	root := codecRoot(t)
	if _, err := os.Stat(filepath.Join(root, "vendor", "index.yaml")); err != nil {
		t.Skip("the codecs were fetched without the device files; give SIGMA_JSCODEC_TTN a checkout")
	}
	r, err := devicerepo.Open(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	vendors, err := r.Vendors()
	if err != nil {
		t.Fatal(err)
	}
	var devices, withCodec, noCodec, broken int
	for _, v := range vendors {
		ds, err := r.Devices(v.ID)
		if errors.Is(err, devicerepo.ErrNotFound) {
			continue // a vendor without devices
		}
		if err != nil {
			t.Errorf("%s: %v", v.ID, err)
			continue
		}
		for _, d := range ds {
			devices++
			c, err := r.Lookup(v.ID, d, "", "")
			switch {
			case errors.Is(err, devicerepo.ErrNotFound):
				noCodec++
				continue
			case err != nil:
				t.Errorf("%s/%s: %v", v.ID, d, err)
				continue
			}
			withCodec++
			if _, err := r.Scripts(c); err != nil {
				broken++
				t.Logf("%s/%s: %v", v.ID, d, err)
			}
		}
	}
	t.Logf("%d vendors, %d devices: %d resolve to a codec, %d have none, %d name a script that is missing", len(vendors), devices, withCodec, noCodec, broken)
	if withCodec == 0 {
		t.Error("no device resolved to a codec")
	}
}
