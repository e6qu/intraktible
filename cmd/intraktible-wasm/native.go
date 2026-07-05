// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !js

// This package is the js/wasm deployment target; build it with
// GOOS=js GOARCH=wasm (see the Makefile's `wasm` target).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "intraktible-wasm is the browser target: build with GOOS=js GOARCH=wasm (make wasm)")
	os.Exit(2)
}
