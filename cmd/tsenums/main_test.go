// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGeneratedEnumsUpToDate is the Go↔TS drift guard: it regenerates the unions
// from the Go consts and compares to the committed enums.generated.ts. If a Go enum
// changed (a value, an added/removed const) without rerunning `make tsenums`, this
// fails — making Go↔TS enum drift impossible to merge. (Runs as part of the normal
// test suite / pre-push gate.)
func TestGeneratedEnumsUpToDate(t *testing.T) {
	// The test runs in the package dir (cmd/tsenums); the output lives at the repo root.
	path := filepath.Join("..", "..", outPath)
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got := Generate(); string(committed) != got {
		t.Fatalf("%s is stale — run `make tsenums` to regenerate it from the Go enum constants", outPath)
	}
}
