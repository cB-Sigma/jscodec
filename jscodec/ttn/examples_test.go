// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package ttn runs real device makers' payload codecs from
// TheThingsNetwork/lorawan-devices (fetched at test time; fetch_test.go and
// testdata/SOURCE.txt) through jscodec and
// checks each against the examples its maker published in the codec's YAML
// file. The comparison follows the repository's own validator: the
// routine's result must equal the example output once both are plain JSON.
//
// Run with: go test -v -run TestCodecExamples
package ttn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cB-Sigma/jscodec"
	"gopkg.in/yaml.v3"
)

type example struct {
	Description      string         `yaml:"description"`
	Input            map[string]any `yaml:"input"`
	Output           map[string]any `yaml:"output"`
	NormalizedOutput map[string]any `yaml:"normalizedOutput"`
}

type routine struct {
	FileName string    `yaml:"fileName"`
	Examples []example `yaml:"examples"`
}

type codecFile struct {
	UplinkDecoder   *routine `yaml:"uplinkDecoder"`
	DownlinkEncoder *routine `yaml:"downlinkEncoder"`
	DownlinkDecoder *routine `yaml:"downlinkDecoder"`
}

// run is one example of one routine of one codec file.
type run struct {
	id      string // vendor/file routine index, the key of knownFailures
	vendor  string
	routine string // decodeUplink, normalizeUplink, encodeDownlink, decodeDownlink
	script  string // path of the JavaScript file
	ex      example
}

func loadRuns(t *testing.T) (runs []run, codecs map[string]int, scripts []string) {
	t.Helper()
	vendorDir := filepath.Join(codecRoot(t), "vendor")
	files, err := filepath.Glob(filepath.Join(vendorDir, "*", "*codec.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no codec files under %s: %v", vendorDir, err)
	}
	sort.Strings(files)
	codecs = map[string]int{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var cf codecFile
		if err := yaml.Unmarshal(b, &cf); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		vendor := filepath.Base(filepath.Dir(f))
		name := vendor + "/" + filepath.Base(f)
		codecs[vendor]++
		add := func(r *routine, fn string) {
			if r == nil {
				return
			}
			script := filepath.Join(vendorDir, vendor, filepath.FromSlash(r.FileName))
			scripts = append(scripts, script)
			for i, ex := range r.Examples {
				runs = append(runs, run{id: fmt.Sprintf("%s %s %d", name, fn, i), vendor: vendor, routine: fn, script: script, ex: ex})
				if fn == "decodeUplink" && ex.NormalizedOutput != nil {
					runs = append(runs, run{id: fmt.Sprintf("%s normalizeUplink %d", name, i), vendor: vendor,
						routine: "normalizeUplink", script: script,
						ex: example{Description: ex.Description, Input: ex.Output, Output: ex.NormalizedOutput}})
				}
			}
		}
		add(cf.UplinkDecoder, "decodeUplink")
		add(cf.DownlinkEncoder, "encodeDownlink")
		add(cf.DownlinkDecoder, "decodeDownlink")
	}
	return runs, codecs, scripts
}

// cache compiles each script once per test and isolation mode.
type cache struct {
	iso jscodec.Isolation
	m   map[string]*jscodec.Codec
}

func newCache(t *testing.T, iso jscodec.Isolation) *cache {
	c := &cache{iso: iso, m: map[string]*jscodec.Codec{}}
	t.Cleanup(func() {
		for _, cc := range c.m {
			_ = cc.Close()
		}
	})
	return c
}

func (k *cache) codec(script string) (*jscodec.Codec, error) {
	if c, ok := k.m[script]; ok {
		return c, nil
	}
	src, err := os.ReadFile(script)
	if err != nil {
		return nil, err
	}
	// The repository's validator gives each run one second.
	c, err := jscodec.New(filepath.Base(script), string(src), jscodec.Limits{Timeout: time.Second, Isolation: k.iso, Workers: 1}, nil)
	if err != nil {
		return nil, err
	}
	k.m[script] = c
	return c, nil
}

// TestMain lets the test binary serve as the codec worker process.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == jscodec.WorkerCommand {
		os.Exit(jscodec.RunWorker())
	}
	os.Exit(m.Run())
}

// plainJSON turns a value into what JSON.parse would give: maps, slices,
// float64, string, bool, nil.
func plainJSON(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<not JSON: %v>", err)
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func strs(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, e := range l {
		out = append(out, fmt.Sprint(e))
	}
	return out
}

func equalStrs(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func toBytes(v any) ([]byte, error) {
	l, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("bytes %v is not a list", v)
	}
	out := make([]byte, len(l))
	for i, e := range l {
		var n int
		switch x := e.(type) {
		case int:
			n = x
		case float64:
			n = int(x)
		default:
			return nil, fmt.Errorf("byte %d (%v) is not a number", i, e)
		}
		out[i] = byte(n)
	}
	return out, nil
}

func fport(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	}
	return 0
}

// check runs one example and returns why it does not match, or "".
func check(ctx context.Context, k *cache, r run) string {
	c, err := k.codec(r.script)
	if err != nil {
		return "loading the script: " + err.Error()
	}
	in, want := r.ex.Input, r.ex.Output
	for k := range in {
		switch k {
		case "bytes", "fPort", "recvTime", "data", "warnings", "errors":
		default:
			return "the example input has a field the test does not pass on: " + k
		}
	}
	wantErrs := strs(want["errors"])
	wantWarn := strs(want["warnings"])
	// result compares an error and the decoded fields with the example.
	result := func(err error, data any, warnings []string) string {
		if len(wantErrs) > 0 {
			var de *jscodec.DecoderError
			if !errors.As(err, &de) {
				return fmt.Sprintf("want errors %q, got error %v and data %v", wantErrs, err, plainJSON(data))
			}
			if !equalStrs(de.Errors, wantErrs) {
				return fmt.Sprintf("want errors %q, got %q", wantErrs, de.Errors)
			}
			return ""
		}
		if err != nil {
			return "error: " + err.Error()
		}
		if g, w := plainJSON(data), plainJSON(want["data"]); !reflect.DeepEqual(g, w) {
			gb, _ := json.Marshal(g)
			wb, _ := json.Marshal(w)
			return fmt.Sprintf("data differs:\n got %s\nwant %s", gb, wb)
		}
		if !equalStrs(warnings, wantWarn) {
			return fmt.Sprintf("want warnings %q, got %q", wantWarn, warnings)
		}
		return ""
	}
	switch r.routine {
	case "decodeUplink", "decodeDownlink":
		b, err := toBytes(in["bytes"])
		if err != nil {
			return "example input: " + err.Error()
		}
		var d *jscodec.Decoded
		if r.routine == "decodeUplink" {
			u := jscodec.Uplink{Bytes: b, FPort: fport(in["fPort"])}
			if s, ok := in["recvTime"].(string); ok {
				if u.RecvTime, err = time.Parse(time.RFC3339Nano, s); err != nil {
					return "example input: " + err.Error()
				}
			}
			d, err = c.Decode(ctx, u)
		} else {
			d, err = c.DecodeDownlink(ctx, jscodec.DownlinkFrame{Bytes: b, FPort: fport(in["fPort"])})
		}
		if d == nil {
			return result(err, nil, nil)
		}
		var data any
		if d.Data != nil {
			data = d.Data
		}
		return result(err, data, d.Warnings)
	case "normalizeUplink":
		data, _ := plainJSON(in["data"]).(map[string]any)
		n, err := c.Normalize(ctx, data)
		if n == nil {
			return result(err, nil, nil)
		}
		// The validator wraps a single object in a list; so does Normalize.
		if w, ok := want["data"].(map[string]any); ok {
			want = map[string]any{"data": []any{w}, "warnings": want["warnings"]}
		}
		var got any
		if n.Data != nil {
			got = n.Data
		}
		return result(err, got, n.Warnings)
	case "encodeDownlink":
		e, err := c.Encode(ctx, jscodec.Downlink{Data: plainJSON(in["data"]), FPort: fport(in["fPort"])})
		if len(wantErrs) > 0 || err != nil {
			return result(err, nil, nil)
		}
		wb, err := toBytes(want["bytes"])
		if err != nil {
			return "example output: " + err.Error()
		}
		if !reflect.DeepEqual(e.Bytes, wb) && !(len(e.Bytes) == 0 && len(wb) == 0) {
			return fmt.Sprintf("bytes differ:\n got % X\nwant % X", e.Bytes, wb)
		}
		if e.FPort != fport(want["fPort"]) {
			return fmt.Sprintf("want fPort %v, got %d", want["fPort"], e.FPort)
		}
		if !equalStrs(e.Warnings, wantWarn) {
			return fmt.Sprintf("want warnings %q, got %q", wantWarn, e.Warnings)
		}
		return ""
	}
	return "unknown routine " + r.routine
}

// TestCodecExamples runs every example in process and, unless -short, in
// worker processes, which must give the same results.
func TestCodecExamples(t *testing.T) {
	t.Run(string(jscodec.InProcess), func(t *testing.T) { runExamples(t, jscodec.InProcess) })
	t.Run(string(jscodec.Process), func(t *testing.T) {
		if testing.Short() {
			t.Skip("process isolation is skipped with -short")
		}
		if runtime.GOOS == "windows" {
			t.Skip("process isolation is not supported on Windows")
		}
		runExamples(t, jscodec.Process)
	})
}

func runExamples(t *testing.T, iso jscodec.Isolation) {
	runs, codecs, scripts := loadRuns(t)
	ctx := context.Background()
	cache := newCache(t, iso)
	// Every script a codec file names must load, examples or not.
	for _, s := range scripts {
		if _, err := cache.codec(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	type tally struct{ examples, passed, known int }
	per := map[string]*tally{}
	seen := map[string]bool{}
	for _, r := range runs {
		seen[r.id] = true
		if per[r.vendor] == nil {
			per[r.vendor] = &tally{}
		}
		tl := per[r.vendor]
		tl.examples++
		why := check(ctx, cache, r)
		k, listed := knownFailures[r.id]
		switch {
		case why == "" && listed:
			t.Errorf("%s (%s) now passes; remove it from knownFailures", r.id, r.ex.Description)
			tl.passed++
		case why == "":
			tl.passed++
		case listed:
			tl.known++
			t.Logf("known failure %s [%s]: %s", r.id, k.category, firstLine(why))
		default:
			t.Errorf("%s (%s): %s", r.id, r.ex.Description, why)
		}
	}
	for id := range knownFailures {
		if !seen[id] {
			t.Errorf("knownFailures lists %s, which is not an example", id)
		}
	}
	vendors := make([]string, 0, len(per))
	for v := range per {
		vendors = append(vendors, v)
	}
	sort.Strings(vendors)
	var sb strings.Builder
	var tc, te, tp int
	fmt.Fprintf(&sb, "\n| Vendor | Codecs | Examples | Passed |\n|---|---|---|---|\n")
	for _, v := range vendors {
		tl := per[v]
		fmt.Fprintf(&sb, "| %s | %d | %d | %d |\n", v, codecs[v], tl.examples, tl.passed)
		tc, te, tp = tc+codecs[v], te+tl.examples, tp+tl.passed
	}
	fmt.Fprintf(&sb, "| total | %d | %d | %d |\n", tc, te, tp)
	byRoutine := map[string]int{}
	for _, r := range runs {
		byRoutine[r.routine]++
	}
	fmt.Fprintf(&sb, "examples by routine: %v\n", byRoutine)
	t.Log(sb.String())
}

// TestCheckIsStrict guards the comparison itself: every passing example
// must fail once its expected output is altered.
func TestCheckIsStrict(t *testing.T) {
	runs, _, _ := loadRuns(t)
	ctx := context.Background()
	cache := newCache(t, jscodec.InProcess)
	for _, r := range runs {
		if _, listed := knownFailures[r.id]; listed || check(ctx, cache, r) != "" {
			continue
		}
		var alt []map[string]any
		out := r.ex.Output
		with := func(k string, v any) map[string]any {
			m := map[string]any{}
			for kk, vv := range out {
				m[kk] = vv
			}
			m[k] = v
			return m
		}
		switch {
		case len(strs(out["errors"])) > 0:
			alt = append(alt, with("errors", []any{"jscodec probe"}), with("errors", []any{}))
		case r.routine == "encodeDownlink":
			b, _ := out["bytes"].([]any)
			alt = append(alt, with("bytes", append(append([]any{}, b...), 0)), with("fPort", fport(out["fPort"])+1),
				with("warnings", []any{"jscodec probe"}))
		default:
			alt = append(alt, with("data", map[string]any{"jscodec probe": 1}), with("warnings", []any{"jscodec probe"}))
			if d, ok := out["data"].(map[string]any); ok && len(d) > 0 {
				m := map[string]any{}
				for k, v := range d {
					m[k] = v
				}
				for k := range m {
					delete(m, k)
					break
				}
				alt = append(alt, with("data", m))
			}
		}
		for i, o := range alt {
			rr := r
			rr.ex.Output = o
			if check(ctx, cache, rr) == "" {
				t.Errorf("%s: altered expectation %d still passes", r.id, i)
			}
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}
