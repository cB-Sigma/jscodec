// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package devicerepo finds a device's payload codec in a copy of The Things
// Network's LoRaWAN device repository (github.com/TheThingsNetwork/
// lorawan-devices) or any directory laid out the same way: vendor/index.yaml
// lists the vendors; vendor/<vendor>/index.yaml lists a vendor's devices;
// vendor/<vendor>/<device>.yaml gives each firmware version's profile per
// region with the codec's identifier; vendor/<vendor>/<codec>.yaml names
// the scripts of the codec's routines and their examples. The layout facts
// are read from the repository's files and its schema; the package reads
// them from an fs.FS (os.DirFS of a checkout, an embedded copy) and does
// not fetch anything.
package devicerepo

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Repo is a device repository.
type Repo struct {
	fsys fs.FS
}

// Open reads a repository whose root holds vendor/.
func Open(fsys fs.FS) (*Repo, error) {
	if _, err := fs.Stat(fsys, "vendor/index.yaml"); err != nil {
		return nil, fmt.Errorf("devicerepo: no vendor/index.yaml: %w", err)
	}
	return &Repo{fsys: fsys}, nil
}

// Vendor is one vendor of the index.
type Vendor struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	VendorID int    `yaml:"vendorID"`
	Draft    bool   `yaml:"draft"`
}

// ErrNotFound is returned for an unknown vendor, device, firmware, region
// or codec.
var ErrNotFound = errors.New("devicerepo: not found")

func (r *Repo) yaml(p string, v any) error {
	b, err := fs.ReadFile(r.fsys, p)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotFound, p)
	}
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(b, v); err != nil {
		return fmt.Errorf("devicerepo: %s: %w", p, err)
	}
	return nil
}

func safe(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return fmt.Errorf("devicerepo: %q is not an identifier", id)
	}
	return nil
}

// Vendors lists the vendors (drafts too; see Vendor.Draft).
func (r *Repo) Vendors() ([]Vendor, error) {
	var idx struct {
		Vendors []Vendor `yaml:"vendors"`
	}
	if err := r.yaml("vendor/index.yaml", &idx); err != nil {
		return nil, err
	}
	return idx.Vendors, nil
}

// Devices lists a vendor's device identifiers.
func (r *Repo) Devices(vendor string) ([]string, error) {
	if err := safe(vendor); err != nil {
		return nil, err
	}
	var idx struct {
		EndDevices []string `yaml:"endDevices"`
	}
	if err := r.yaml(path.Join("vendor", vendor, "index.yaml"), &idx); err != nil {
		return nil, err
	}
	return idx.EndDevices, nil
}

// Profile is a device's profile in one region.
type Profile struct {
	ID               string `yaml:"id"`
	Codec            string `yaml:"codec"`
	LoRaWANCertified bool   `yaml:"lorawanCertified"`
	// VendorID names the vendor whose folder holds the profile, when it
	// is not the device's (the codec stays the device vendor's).
	VendorID string `yaml:"vendorID"`
}

// Firmware is one firmware version of a device.
type Firmware struct {
	Version          string             `yaml:"version"`
	HardwareVersions []string           `yaml:"hardwareVersions"`
	Profiles         map[string]Profile `yaml:"profiles"`
}

// Device is a device's description.
type Device struct {
	Name             string     `yaml:"name"`
	Description      string     `yaml:"description"`
	FirmwareVersions []Firmware `yaml:"firmwareVersions"`
	Sensors          []string   `yaml:"sensors"`
}

// Device reads a device's description.
func (r *Repo) Device(vendor, device string) (*Device, error) {
	if err := safe(vendor); err != nil {
		return nil, err
	}
	if err := safe(device); err != nil {
		return nil, err
	}
	var d Device
	if err := r.yaml(path.Join("vendor", vendor, device+".yaml"), &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Example is one example of a codec routine as the repository gives it.
type Example struct {
	Description      string         `yaml:"description"`
	Input            map[string]any `yaml:"input"`
	Output           map[string]any `yaml:"output"`
	NormalizedOutput map[string]any `yaml:"normalizedOutput"`
}

// Routine is one routine of a codec: the script file and its examples.
type Routine struct {
	FileName string    `yaml:"fileName"`
	Examples []Example `yaml:"examples"`
}

// Codec is a codec definition.
type Codec struct {
	ID              string
	Vendor          string
	UplinkDecoder   *Routine `yaml:"uplinkDecoder"`
	DownlinkEncoder *Routine `yaml:"downlinkEncoder"`
	DownlinkDecoder *Routine `yaml:"downlinkDecoder"`
}

// Codec reads a codec definition of a vendor.
func (r *Repo) Codec(vendor, id string) (*Codec, error) {
	if err := safe(vendor); err != nil {
		return nil, err
	}
	if err := safe(id); err != nil {
		return nil, err
	}
	c := &Codec{}
	if err := r.yaml(path.Join("vendor", vendor, id+".yaml"), c); err != nil {
		return nil, err
	}
	c.ID, c.Vendor = id, vendor
	return c, nil
}

// Scripts returns the codec's JavaScript sources by file name, read once
// each (routines often share one file).
func (r *Repo) Scripts(c *Codec) (map[string]string, error) {
	out := map[string]string{}
	for _, rt := range []*Routine{c.UplinkDecoder, c.DownlinkEncoder, c.DownlinkDecoder} {
		if rt == nil || rt.FileName == "" {
			continue
		}
		if _, ok := out[rt.FileName]; ok {
			continue
		}
		p := path.Clean(path.Join("vendor", c.Vendor, rt.FileName))
		if !strings.HasPrefix(p, "vendor/"+c.Vendor+"/") {
			return nil, fmt.Errorf("devicerepo: script %q leaves the vendor's folder", rt.FileName)
		}
		b, err := fs.ReadFile(r.fsys, p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
		}
		if err != nil {
			return nil, err
		}
		out[rt.FileName] = string(b)
	}
	return out, nil
}

// Lookup finds the codec of a device: firmware "" takes the first version
// listed, region "" the first profile with a codec in name order (the
// repository uses band names such as "EU863-870").
func (r *Repo) Lookup(vendor, device, firmware, region string) (*Codec, error) {
	d, err := r.Device(vendor, device)
	if err != nil {
		return nil, err
	}
	for _, fw := range d.FirmwareVersions {
		if firmware != "" && fw.Version != firmware {
			continue
		}
		regions := make([]string, 0, len(fw.Profiles))
		for reg := range fw.Profiles {
			regions = append(regions, reg)
		}
		sort.Strings(regions)
		for _, reg := range regions {
			p := fw.Profiles[reg]
			if (region != "" && reg != region) || p.Codec == "" {
				continue
			}
			// A profile may live in another vendor's folder (VendorID); the
			// codec is the device vendor's.
			return r.Codec(vendor, p.Codec)
		}
		return nil, fmt.Errorf("%w: no codec for %s/%s firmware %s region %q", ErrNotFound, vendor, device, fw.Version, region)
	}
	return nil, fmt.Errorf("%w: %s/%s has no firmware %q", ErrNotFound, vendor, device, firmware)
}
