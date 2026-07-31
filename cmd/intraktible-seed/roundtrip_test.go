// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/e6qu/intraktible/platform/erasure"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/server"
)

// TestSeedAssetReplays boots the generated demo seed through the exact wasm
// path — restore operational keys, then NewMemoryFrom + server.New over a fresh
// in-memory store — and spot-checks the rebuilt projections. Drift between the
// assets and replay/projection code fails loudly; regenerate with
// `make demo-seed`.
func TestSeedAssetReplays(t *testing.T) {
	path := filepath.Join("..", "..", "web", "static", "demo-seed.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("demo seed asset not present (%v) — generate it with `make demo-seed`", err)
	}
	var events []eventlog.Envelope
	if err := json.Unmarshal(b, &events); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	statePath := filepath.Join("..", "..", "web", "static", "demo-state.json")
	stateJSON, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read %s: %v — regenerate with `make demo-seed`", statePath, err)
	}
	var operationalState erasure.OperationalState
	if err := json.Unmarshal(stateJSON, &operationalState); err != nil {
		t.Fatalf("decode %s: %v", statePath, err)
	}
	log, err := eventlog.NewMemoryFrom(events)
	if err != nil {
		t.Fatalf("replay %s: %v", path, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	st := store.NewMemory()
	if err := erasure.RestoreOperationalState(ctx, st, operationalState); err != nil {
		cancel()
		t.Fatalf("restore demo operational state: %v", err)
	}
	srv, err := server.New(ctx, server.Config{Modules: "all", DevAPIKey: devAPIKey, StoreKind: "memory"},
		log, st)
	if err != nil {
		cancel()
		t.Fatalf("assemble over the seed: %v", err)
	}
	// Cancel before Close: the async-run workers stop on ctx cancellation, and
	// Close waits for them (the recovered "running" run drains here).
	defer func() {
		cancel()
		srv.Close()
	}()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("seed drift: %v", r)
		}
	}()
	t.Logf("replayed %d events: %s", len(events), spotCheck(srv))
}
