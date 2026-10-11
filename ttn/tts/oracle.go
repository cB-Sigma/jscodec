// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

//go:build ignore

// Command oracle runs codec routines with The Things Stack's JavaScript
// runner, the one the lorawan-devices validator uses, one request per
// input line ({"script", "routine", "input"}), printing one result line
// ({"output"} or {"error"}). It is built in the Dockerfile next to it; the
// ttn tests use it as the reference for every example jscodec fails.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"time"

	"go.thethings.network/lorawan-stack/v3/pkg/scripting"
	"go.thethings.network/lorawan-stack/v3/pkg/scripting/javascript"
)

type request struct {
	Script  string         `json:"script"`
	Routine string         `json:"routine"`
	Input   map[string]any `json:"input"`
}

type response struct {
	Output any    `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

func main() {
	opts := scripting.DefaultOptions
	opts.Timeout = time.Second
	vm := javascript.New(opts)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 16<<20), 16<<20)
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var r request
		var out response
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			out.Error = "request: " + err.Error()
		} else if valueAs, err := vm.Run(context.Background(), r.Script, r.Routine, r.Input); err != nil {
			out.Error = err.Error()
		} else {
			var v any
			if err := valueAs(&v); err != nil {
				out.Error = "output: " + err.Error()
			} else {
				out.Output = v
			}
		}
		if err := enc.Encode(out); err != nil {
			_ = enc.Encode(response{Error: "output not JSON: " + err.Error()})
		}
	}
}
