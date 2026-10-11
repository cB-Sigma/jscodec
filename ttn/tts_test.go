// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package ttn

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// ttsImage is The Things Stack's JavaScript runner (v3.36.2), built from
// ./tts: the runner the lorawan-devices validator uses.
const ttsImage = "sigma-jscodec-tts:3.36.2"

type oracleReq struct {
	Script  string         `json:"script"`
	Routine string         `json:"routine"`
	Input   map[string]any `json:"input"`
}

type oracleResp struct {
	Output any    `json:"output"`
	Error  string `json:"error"`
}

// runOracle sends every request through one container and returns the
// answers in order.
func runOracle(t *testing.T, reqs []oracleReq) []oracleResp {
	t.Helper()
	cmd := exec.Command("docker", "run", "--rm", "-i", ttsImage)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		enc := json.NewEncoder(in)
		for _, r := range reqs {
			_ = enc.Encode(r)
		}
		_ = in.Close()
	}()
	var res []oracleResp
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 16<<20), 16<<20)
	for sc.Scan() {
		var r oracleResp
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("oracle: %v", err)
		}
		res = append(res, r)
	}
	_, _ = io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("oracle: %v", err)
	}
	if len(res) != len(reqs) {
		t.Fatalf("oracle answered %d of %d requests", len(res), len(reqs))
	}
	return res
}

// ttsMatches is the lorawan-devices validator's verdict: the runner's
// output deep-equals the example output (normalizeUplink's single object
// wrapped in a list first).
func ttsMatches(r run, o oracleResp) bool {
	if o.Error != "" {
		return false
	}
	got := plainJSON(o.Output)
	if m, ok := got.(map[string]any); ok && r.routine == "normalizeUplink" {
		if d, ok := m["data"]; ok {
			if _, isList := d.([]any); !isList {
				m["data"] = []any{d}
			}
		}
	}
	want := plainJSON(r.ex.Output)
	if w, ok := want.(map[string]any); ok && r.routine == "normalizeUplink" {
		if d, ok := w["data"]; ok {
			if _, isList := d.([]any); !isList {
				w["data"] = []any{d}
			}
		}
	}
	return reflect.DeepEqual(got, want)
}

// TestAgainstTheThingsStack runs every example through The Things Stack's
// runner as well (SIGMA_JSCODEC_TTS=1, Docker): where the runner passes an
// example, jscodec must pass it too, and every known failure must be one
// the runner fails as well, unless it is listed as a jscodec difference
// (category "sandbox" or "api"). It prints both tallies.
// Covers: SIG-IPE-6745
func TestAgainstTheThingsStack(t *testing.T) {
	if os.Getenv("SIGMA_JSCODEC_TTS") != "1" {
		t.Skip("set SIGMA_JSCODEC_TTS=1 (and SIGMA_JSCODEC_TTN) to compare with The Things Stack's runner in Docker")
	}
	if exec.Command("docker", "image", "inspect", ttsImage).Run() != nil {
		if out, err := exec.Command("docker", "build", "-q", "-t", ttsImage, "tts").CombinedOutput(); err != nil {
			t.Fatalf("docker build: %v\n%s", err, out)
		}
	}
	runs, _, _ := loadRuns(t)
	sources := map[string]string{}
	reqs := make([]oracleReq, len(runs))
	for i, r := range runs {
		src, ok := sources[r.script]
		if !ok {
			b, err := os.ReadFile(r.script)
			if err != nil {
				src = "/* missing */"
			} else {
				src = string(b)
			}
			sources[r.script] = src
		}
		in, _ := plainJSON(r.ex.Input).(map[string]any)
		reqs[i] = oracleReq{Script: src, Routine: r.routine, Input: in}
	}
	res := runOracle(t, reqs)
	cache := newCache(t, "")
	var both, onlyTTS, onlyUs, neither int
	var bad []string
	for i, r := range runs {
		tts := ttsMatches(r, res[i])
		us := check(t.Context(), cache, r) == ""
		k, listed := knownFailures[r.id]
		switch {
		case tts && us:
			both++
		case tts && !us:
			onlyTTS++
			if !listed || k.category == "upstream" {
				bad = append(bad, fmt.Sprintf("%s: The Things Stack passes it, jscodec does not", r.id))
			}
		case !tts && us:
			onlyUs++
		default:
			neither++
			if listed && k.category != "upstream" {
				bad = append(bad, fmt.Sprintf("%s: listed as %s, but The Things Stack fails it too (%s)", r.id, k.category, firstLine(res[i].Error)))
			}
		}
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Error(b)
	}
	t.Logf("%d examples: both pass %d; only The Things Stack %d; only jscodec %d; neither %d",
		len(runs), both, onlyTTS, onlyUs, neither)
	if os.Getenv("SIGMA_JSCODEC_TTS_LIST") == "1" {
		var sb strings.Builder
		for i, r := range runs {
			if !ttsMatches(r, res[i]) && check(t.Context(), cache, r) != "" {
				fmt.Fprintf(&sb, "%s\t%s\n", r.id, firstLine(res[i].Error))
			}
		}
		t.Logf("failed by both:\n%s", sb.String())
	}
}
