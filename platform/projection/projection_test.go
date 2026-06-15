// SPDX-License-Identifier: AGPL-3.0-or-later

package projection_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
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

func (counter) Collections() []string { return []string{countCollection} }

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

func (boomer) Name() string { return "boomer" } // writes nothing, owns no collections

func (boomer) Collections() []string { return nil }

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

func TestRebuildToSeqIsBounded(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()

	for _, typ := range []string{"a", "b", "c", "d"} { // seqs 1..4
		appendEvent(t, log, typ)
	}

	// Rebuild as of seq 2 (log-based rollback): only the first two events apply.
	st := store.NewMemory()
	applied, err := projection.New(log, st, counter{}).RebuildTo(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 2 || readCount(t, st) != 2 {
		t.Fatalf("as-of seq 2: applied=%d count=%d, want 2/2", applied, readCount(t, st))
	}

	// upTo 0 replays the whole log.
	full := store.NewMemory()
	applied, err = projection.New(log, full, counter{}).RebuildTo(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 4 || readCount(t, full) != 4 {
		t.Fatalf("full replay: applied=%d count=%d, want 4/4", applied, readCount(t, full))
	}
}

func TestRebuildIsIdempotent(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()

	appendEvent(t, log, "a")
	appendEvent(t, log, "b")

	// Rebuild twice into the SAME store: the runtime resets the projector's
	// collections first, so the second pass does not double-count.
	st := store.NewMemory()
	rt := projection.New(log, st, counter{})
	if _, err := rt.RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if got := readCount(t, st); got != 2 {
		t.Fatalf("after two rebuilds: count=%d, want 2 (reset makes rebuild idempotent)", got)
	}
}

// TestIncrementalResumeWithDurableStore is the point of D21b: a durable
// (transactional) store resumes from its checkpoint instead of fully rebuilding,
// so a non-idempotent projector (the counter) is not re-applied — and no reset
// wipes data the log did not produce.
func TestIncrementalResumeWithDurableStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logDir := filepath.Join(dir, "log")
	dbPath := filepath.Join(dir, "p.db")

	log1, err := eventlog.OpenWAL(logDir)
	if err != nil {
		t.Fatal(err)
	}
	st1, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	appendEvent(t, log1, "a")
	appendEvent(t, log1, "b")

	ctx1, cancel1 := context.WithCancel(ctx)
	rt1 := projection.New(log1, st1, counter{})
	if err := rt1.Start(ctx1); err != nil {
		t.Fatal(err)
	}
	if got := readCount(t, st1); got != 2 {
		t.Fatalf("after first build: count=%d, want 2", got)
	}
	appendEvent(t, log1, "c") // live → checkpoint advances to 3
	if !testutil.Eventually(t, func() bool { return readCount(t, st1) == 3 }) {
		t.Fatal("live event did not reach the projection")
	}
	cancel1()
	_ = st1.Close()
	_ = log1.Close()

	// Restart against the SAME durable store + log. Seed a sentinel the log can
	// never produce: it survives a resume (no reset) but a full rebuild would wipe it.
	log2, err := eventlog.OpenWAL(logDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log2.Close() }()
	st2, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st2.Close() }()
	sentinelKey := store.Key("demo", "main", "sentinel")
	if err := st2.Put(ctx, countCollection, sentinelKey, json.RawMessage(`true`)); err != nil {
		t.Fatal(err)
	}

	rt2 := projection.New(log2, st2, counter{})
	ctx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	if err := rt2.Start(ctx2); err != nil {
		t.Fatal(err)
	}
	// Resume applied nothing new (checkpoint == head): the count is unchanged
	// (not re-applied to 6, not reset to 0) and the sentinel survived (no reset).
	if got := readCount(t, st2); got != 3 {
		t.Fatalf("after resume: count=%d, want 3 (no re-apply, no reset)", got)
	}
	if _, ok, _ := st2.Get(ctx, countCollection, sentinelKey); !ok {
		t.Fatal("resume must NOT reset the collection (the sentinel was wiped → a full rebuild ran)")
	}

	// A new live event advances from the resumed state, counted exactly once.
	appendEvent(t, log2, "d")
	if !testutil.Eventually(t, func() bool { return readCount(t, st2) == 4 }) {
		t.Fatalf("new event after resume: count=%d, want 4", readCount(t, st2))
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
