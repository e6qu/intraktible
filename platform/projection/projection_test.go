// SPDX-License-Identifier: AGPL-3.0-or-later

package projection_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

const countCollection = "proj_test_count"

// counter folds every event into a per-tenant integer count.
type counter struct{}

func (counter) Name() string { return "counter" }

func (counter) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	key := store.Key(e.Org, e.Workspace, "n")
	var n int
	if doc, ok, err := s.Get(ctx, countCollection, key); err != nil {
		return err
	} else if ok {
		if err := json.Unmarshal(doc, &n); err != nil {
			return err
		}
	}
	n++
	doc, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return s.Put(ctx, countCollection, key, doc)
}

// boomer fails on a specific event type, to exercise the fail-loudly path.
type boomer struct{}

func (boomer) Name() string { return "boomer" }

func (boomer) Apply(_ context.Context, e eventlog.Envelope, _ store.Store) error {
	if e.Type == "boom" {
		return errors.New("boom")
	}
	return nil
}

func appendEvent(t *testing.T, log eventlog.Log, typ string) {
	t.Helper()
	if _, err := log.Append(context.Background(), eventlog.Envelope{
		Org: "demo", Workspace: "main", Actor: "t", Stream: "s", Type: typ,
	}); err != nil {
		t.Fatal(err)
	}
}

func readCount(t *testing.T, s store.Store) int {
	t.Helper()
	doc, ok, err := s.Get(context.Background(), countCollection, store.Key("demo", "main", "n"))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return 0
	}
	var n int
	if err := json.Unmarshal(doc, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRebuildThenLive(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()

	appendEvent(t, log, "a")
	appendEvent(t, log, "b")

	st := store.NewMemory()
	rt := projection.New(log, st, counter{})
	if err := rt.Start(ctx); err != nil { // rebuilds from offset 0 synchronously
		t.Fatal(err)
	}
	if got := readCount(t, st); got != 2 {
		t.Fatalf("after rebuild: count=%d, want 2", got)
	}

	appendEvent(t, log, "c") // live, via the bus
	if !testutil.Eventually(t, func() bool { return readCount(t, st) == 3 }) {
		t.Fatal("live event did not reach the projection")
	}
	if err := rt.Err(); err != nil {
		t.Fatalf("unexpected runtime error: %v", err)
	}
}

func TestLiveApplyErrorSurfaced(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()

	st := store.NewMemory()
	rt := projection.New(log, st, boomer{})
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	appendEvent(t, log, "boom")
	if !testutil.Eventually(t, func() bool { return rt.Err() != nil }) {
		t.Fatal("a live apply error must be surfaced via Err (fail loudly)")
	}
}

func TestRebuildErrorIsReturned(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	appendEvent(t, log, "boom")

	rt := projection.New(log, store.NewMemory(), boomer{})
	if err := rt.Start(ctx); err == nil {
		t.Fatal("Start must return the error when rebuild fails")
	}
}
