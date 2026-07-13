// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// failingStore wraps a Store and, once fail is armed, makes every read/write return the
// injected error — a stand-in for a projection store that dies mid-operation (disk full,
// connection dropped). It is safe for concurrent use because the wrapped store is and the
// flag is atomic.
type failingStore struct {
	*store.Memory
	armed atomic.Bool
	err   error
}

func (s *failingStore) arm()  { s.armed.Store(true) }
func (s *failingStore) heal() { s.armed.Store(false) }

func (s *failingStore) Get(ctx context.Context, collection, key string) (json.RawMessage, bool, error) {
	if s.armed.Load() {
		return nil, false, s.err
	}
	return s.Memory.Get(ctx, collection, key)
}

func (s *failingStore) List(ctx context.Context, collection, keyPrefix string) ([]store.Record, error) {
	if s.armed.Load() {
		return nil, s.err
	}
	return s.Memory.List(ctx, collection, keyPrefix)
}

func (s *failingStore) Put(ctx context.Context, collection, key string, doc json.RawMessage) error {
	if s.armed.Load() {
		return s.err
	}
	return s.Memory.Put(ctx, collection, key, doc)
}

// TestDecideFailsLoudOnStoreError closes the store-failure gap the throughput doc flagged:
// a decide whose projection store dies mid-operation must fail loud (an infrastructure
// error, or a non-completed decision) rather than silently completing against a store
// that dropped its reads. The flow is published while the store is healthy, then the
// store is armed to fail before the decide, so the failure is on the decide path itself.
func TestDecideFailsLoudOnStoreError(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	fs := &failingStore{Memory: store.NewMemory(), err: errors.New("store: backing storage unavailable")}
	publishFlow(t, ctx, log, fs, id, "risk", "Risk", flowtest.DecisionGraph())
	dh := command.NewDecideHandler(log, fs)

	// Positive control: healthy store, the decision completes.
	res, err := dh.Decide(ctx, id, "risk", "sandbox", riskInput(), command.EntityRef{})
	if err != nil || res.Status != "completed" {
		t.Fatalf("healthy control should complete, got status=%s err=%v", res.Status, err)
	}

	// Arm the store to fail, then decide: it must fail loud, not complete.
	fs.arm()
	res, err = dh.Decide(ctx, id, "risk", "sandbox", riskInput(), command.EntityRef{})
	if !failedLoud(res, err) {
		t.Fatalf("a dead store must not yield a completed decision, got status=%s", res.Status)
	}
	if err != nil && !strings.Contains(err.Error(), "backing storage unavailable") {
		t.Fatalf("the store failure should surface, got %v", err)
	}

	// Heal it: the handler recovers on the next decide (the failure was not sticky).
	fs.heal()
	res, err = dh.Decide(ctx, id, "risk", "sandbox", riskInput(), command.EntityRef{})
	if err != nil || res.Status != "completed" {
		t.Fatalf("after healing, decide should complete again, got status=%s err=%v", res.Status, err)
	}
}

// TestDecideSoak runs sustained concurrent decide load over the real segmented WAL with a
// small segment cap, so segments rotate and archive continuously underneath the load. It
// asserts no drift after thousands of decides: the log stays gap-free (seq 1..head with
// no holes or duplicates), Head matches the number of records actually on disk, and the
// compaction machinery really engaged (multiple segments, some compressed) rather than the
// log silently growing one file. Skipped under -short.
func TestDecideSoak(t *testing.T) {
	if testing.Short() {
		t.Skip("soak test; run without -short")
	}
	ctx := context.Background()
	// A 4 KiB segment cap and keepHot=2 force frequent rotation + archival under load.
	log, err := eventlog.OpenWALSized(t.TempDir(), 4096, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFlow(t, ctx, log, st, id, "risk", "Risk", flowtest.DecisionGraph())
	dh := command.NewDecideHandler(log, st)

	const workers, perWorker = 4, 250
	var completed atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perWorker {
				res, err := dh.Decide(ctx, id, "risk", "sandbox", riskInput(), command.EntityRef{})
				if err != nil {
					t.Errorf("decide under load failed: %v", err)
					return
				}
				if res.Status != "completed" {
					t.Errorf("decide under load did not complete: %s", res.Status)
					return
				}
				completed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := completed.Load(); got != workers*perWorker {
		t.Fatalf("completed %d decides, want %d", got, workers*perWorker)
	}

	// The full history must read back as a gap-free, duplicate-free 1..head run — a
	// rotation or archival that dropped, reordered, or double-counted a record shows up
	// here as a hole or a mismatched count.
	head := log.Head()
	evs, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatalf("read full log after soak: %v", err)
	}
	if uint64(len(evs)) != head {
		t.Fatalf("read %d events but Head()=%d — the log drifted under load", len(evs), head)
	}
	for i, e := range evs {
		if e.Seq != uint64(i+1) {
			t.Fatalf("gap/duplicate at index %d: seq %d, want %d", i, e.Seq, i+1)
		}
	}

	info := log.Info()
	if info.Segments < 2 {
		t.Fatalf("a 4 KiB cap under thousands of decides should have rotated, got %d segment(s)", info.Segments)
	}
	if info.Compressed == 0 {
		t.Fatal("keepHot=2 under sustained load should have archived older segments, got 0 compressed")
	}
	if info.Head != head {
		t.Fatalf("Info().Head=%d disagrees with Head()=%d", info.Head, head)
	}
}
