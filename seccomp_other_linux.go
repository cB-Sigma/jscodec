// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

//go:build linux && !(amd64 || arm64)

package jscodec

import "errors"

func installSeccomp() error { return errors.New("seccomp: architecture not supported") }
