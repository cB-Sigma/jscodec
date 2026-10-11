// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package jscodec_test

import (
	"context"
	"fmt"

	"github.com/cB-Sigma/jscodec"
)

// Decode an uplink with a TTN-style decodeUplink script, in process.
func Example() {
	src := `function decodeUplink(input) {
	  return { data: { temperature: (input.bytes[0] << 8 | input.bytes[1]) / 10 } };
	}`
	c, err := jscodec.New("demo.js", src, jscodec.Limits{}, nil)
	if err != nil {
		panic(err)
	}
	defer c.Close()
	d, err := c.Decode(context.Background(), jscodec.Uplink{Bytes: []byte{0x00, 0xEB}, FPort: 1})
	if err != nil {
		panic(err)
	}
	fmt.Println(d.Data["temperature"])
	// Output: 23.5
}
