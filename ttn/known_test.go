// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package ttn

// known is why an example does not match. Categories:
//
//   - sandbox: jscodec behaves differently from The Things Stack's runner.
//   - upstream: the codec or its example is wrong; the codec does not
//     produce the published output in The Things Stack's runner either.
//   - api: jscodec's API cannot express what the example compares.
type known struct {
	category string
	why      string
}

// knownFailures lists the examples expected to fail, by "vendor/file
// routine index". The test fails when one of them passes or when any other
// example fails.
var knownFailures = map[string]known{}
