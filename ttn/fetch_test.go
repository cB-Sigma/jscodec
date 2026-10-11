// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package ttn

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The codecs are not part of this repository: lorawan-devices has no
// LICENSE file, so they are fetched at test time from a pinned commit and
// checked against the SHA-256 of each file in testdata/files.sha256.
const (
	upstreamRepo   = "TheThingsNetwork/lorawan-devices"
	upstreamCommit = "900afb52024f795f073ee4192d845af12d2168d0"
	enableVar      = "SIGMA_JSCODEC_TTN"
)

var (
	rootOnce sync.Once
	rootDir  string
	rootSkip string // why the codecs are not available ("" when they are)
	rootErr  string // a fault (wrong content), as opposed to a skip
)

// codecRoot returns a directory holding vendor/... at upstreamCommit, or
// skips the test. SIGMA_JSCODEC_TTN=1 fetches the files into the user
// cache directory (kept for later runs); SIGMA_JSCODEC_TTN=<dir> uses a
// checkout of lorawan-devices at that commit instead.
func codecRoot(t *testing.T) string {
	t.Helper()
	rootOnce.Do(prepareRoot)
	if rootErr != "" {
		t.Fatal(rootErr)
	}
	if rootSkip != "" {
		t.Skip(rootSkip)
	}
	return rootDir
}

type manifestEntry struct{ sum, path string }

func readManifest() ([]manifestEntry, error) {
	f, err := os.Open(filepath.Join("testdata", "files.sha256"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []manifestEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		sum, path, ok := strings.Cut(strings.TrimSpace(sc.Text()), "  ")
		if !ok || strings.HasPrefix(sum, "#") {
			continue
		}
		if strings.Contains(path, "..") || !strings.HasPrefix(path, "vendor/") {
			return nil, fmt.Errorf("files.sha256: bad path %q", path)
		}
		out = append(out, manifestEntry{sum, path})
	}
	return out, sc.Err()
}

func fileSum(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func prepareRoot() {
	v := os.Getenv(enableVar)
	if v == "" {
		rootSkip = fmt.Sprintf("%s is not set: set it to 1 to fetch the makers' codecs from github.com/%s at commit %s (network needed), or to a checkout of that commit", enableVar, upstreamRepo, upstreamCommit[:12])
		return
	}
	files, err := readManifest()
	if err != nil {
		rootErr = err.Error()
		return
	}
	if st, err := os.Stat(filepath.Join(v, "vendor")); err == nil && st.IsDir() {
		for _, e := range files {
			if got := fileSum(filepath.Join(v, filepath.FromSlash(e.path))); got != e.sum {
				rootErr = fmt.Sprintf("%s in %s does not match commit %s (sha256 %s, want %s)", e.path, v, upstreamCommit[:12], got, e.sum)
				return
			}
		}
		rootDir = v
		return
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	dir := filepath.Join(cache, "cb-sigma-jscodec-ttn", upstreamCommit)
	client := &http.Client{Timeout: 30 * time.Second}
	for _, e := range files {
		dst := filepath.Join(dir, filepath.FromSlash(e.path))
		if fileSum(dst) == e.sum {
			continue
		}
		b, err := download(client, e.path)
		if err != nil {
			rootSkip = fmt.Sprintf("cannot fetch the codecs (offline?): %v", err)
			return
		}
		s := sha256.Sum256(b)
		if got := hex.EncodeToString(s[:]); got != e.sum {
			rootErr = fmt.Sprintf("%s at commit %s has sha256 %s, want %s", e.path, upstreamCommit[:12], got, e.sum)
			return
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			rootErr = err.Error()
			return
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			rootErr = err.Error()
			return
		}
	}
	rootDir = dir
}

func download(client *http.Client, path string) ([]byte, error) {
	url := "https://raw.githubusercontent.com/" + upstreamRepo + "/" + upstreamCommit + "/" + path
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}
