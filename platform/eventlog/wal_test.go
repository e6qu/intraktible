// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestWALRecoversTornFinalWrite simulates a crash mid-Append (a trailing record
// with no newline): reopening must recover by dropping the unacknowledged tail,
// and subsequent appends must continue cleanly.
func TestWALRecoversTornFinalWrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"a", "b"} {
		if _, err := w.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: typ}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Append a torn record (no trailing newline), as a crash would leave.
	f, err := os.OpenFile(filepath.Join(dir, "events.log"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"org":"o","workspace":"w","type":"torn","seq":3`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	w2, err := OpenWAL(dir)
	if err != nil {
		t.Fatalf("reopen after a torn write should recover, got: %v", err)
	}
	if w2.Head() != 2 {
		t.Fatalf("head after recovery = %d, want 2", w2.Head())
	}
	if _, err := w2.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "c"}); err != nil {
		t.Fatalf("append after recovery: %v", err)
	}
	evs, _ := w2.Read(ctx, 0)
	if len(evs) != 3 || evs[2].Type != "c" || evs[2].Seq != 3 {
		t.Fatalf("after recovery+append: %+v", evs)
	}
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen once more: the recovered + new state must be clean and complete.
	w3, err := OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w3.Close() }()
	if w3.Head() != 3 {
		t.Fatalf("head after clean reopen = %d, want 3", w3.Head())
	}
}

// faultFile wraps the WAL's file to inject failures at the durability boundary.
type faultFile struct {
	walFile
	syncErr     error
	truncateErr error
}

func (f *faultFile) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.walFile.Sync()
}

func (f *faultFile) Truncate(size int64) error {
	if f.truncateErr != nil {
		return f.truncateErr
	}
	return f.walFile.Truncate(size)
}

// TestWALAppendFsyncFailureLeavesNoGhost guards the fsync-failure invariant: a
// record whose Append the caller saw fail must not linger in the file — it would
// mis-index the next successful append (all later reads "corrupt record") and
// replay as a ghost after reopen.
func TestWALAppendFsyncFailureLeavesNoGhost(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "a"}); err != nil {
		t.Fatal(err)
	}
	ff := &faultFile{walFile: w.f, syncErr: errors.New("injected fsync failure")}
	w.f = ff
	if _, err := w.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "ghost"}); err == nil {
		t.Fatal("append must fail when fsync fails")
	}
	if w.Head() != 1 {
		t.Fatalf("head after failed append = %d, want 1", w.Head())
	}

	ff.syncErr = nil
	e, err := w.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "b"})
	if err != nil {
		t.Fatalf("append after recovered fsync: %v", err)
	}
	if e.Seq != 2 {
		t.Fatalf("seq after recovered append = %d, want 2", e.Seq)
	}
	evs, err := w.Read(ctx, 0)
	if err != nil {
		t.Fatalf("read after recovered append: %v", err)
	}
	if len(evs) != 2 || evs[0].Type != "a" || evs[1].Type != "b" {
		t.Fatalf("after recovered append: %+v", evs)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen: the ghost must not replay.
	w2, err := OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w2.Close() }()
	if w2.Head() != 2 {
		t.Fatalf("head after reopen = %d, want 2", w2.Head())
	}
	evs, err = w2.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Type != "a" || evs[1].Type != "b" {
		t.Fatalf("after reopen: %+v", evs)
	}
}

// TestWALPoisonedWhenRollbackFails: if the post-failure rollback itself fails,
// the file still holds unacknowledged bytes, so further appends must fail loudly
// (until a reopen re-indexes) instead of writing after the ghost.
func TestWALPoisonedWhenRollbackFails(t *testing.T) {
	ctx := context.Background()
	w, err := OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	ff := &faultFile{
		walFile:     w.f,
		syncErr:     errors.New("injected fsync failure"),
		truncateErr: errors.New("injected truncate failure"),
	}
	w.f = ff
	if _, err := w.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "a"}); err == nil {
		t.Fatal("append must fail when fsync fails")
	}
	ff.syncErr, ff.truncateErr = nil, nil
	if _, err := w.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "b"}); err == nil {
		t.Fatal("append after a failed rollback must stay failed until reopen")
	}
	if w.Head() != 0 {
		t.Fatalf("head = %d, want 0", w.Head())
	}
}

// TestWALFailsOnMidFileCorruption confirms a corrupt complete record (not a torn
// tail) still fails loudly rather than being silently skipped. Records are now
// validated lazily, so the failure surfaces on Read — which the projection
// runtime performs over the whole log at boot, so corruption still stops startup.
func TestWALFailsOnMidFileCorruption(t *testing.T) {
	dir := t.TempDir()
	bad := "not valid json\n" + `{"org":"o","workspace":"w","type":"a","seq":2}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.log"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWAL(dir)
	if err != nil {
		t.Fatalf("open indexes lazily and should not decode/fail: %v", err)
	}
	defer func() { _ = w.Close() }()
	if _, err := w.Read(context.Background(), 0); err == nil {
		t.Fatal("a corrupt complete record should fail loudly on Read")
	}
}

// A Unique claim must survive a restart: the reopened WAL rebuilds its claim set from
// disk, so a key already on the log is still rejected (Append's cross-log uniqueness
// contract — a stale optimistic-concurrency claim must not be re-accepted after boot).
func TestWALUniqueClaimSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	w, err := OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "claim", Unique: "k1"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	w2, err := OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w2.Close() }()
	// The same key, reused after the restart, must still conflict.
	if _, err := w2.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "claim", Unique: "k1"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a Unique key claimed before restart must conflict after reopen, got %v", err)
	}
	// A fresh key still succeeds.
	if _, err := w2.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "claim", Unique: "k2"}); err != nil {
		t.Fatalf("a fresh key should append: %v", err)
	}
}

func TestWALAppendReadReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	w, err := OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	e1, err := w.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "t", Payload: json.RawMessage(`{"n":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if e1.Seq != 1 {
		t.Fatalf("seq = %d, want 1", e1.Seq)
	}
	if _, err := w.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "t", Payload: json.RawMessage(`{"n":2}`)}); err != nil {
		t.Fatal(err)
	}
	got, err := w.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d events, want 2", len(got))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen: durability + monotonic seq across restarts.
	w2, err := OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w2.Close() }()
	if w2.Head() != 2 {
		t.Fatalf("head after reopen = %d, want 2", w2.Head())
	}
	e3, err := w2.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "t", Payload: json.RawMessage(`{"n":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	if e3.Seq != 3 {
		t.Fatalf("seq after reopen = %d, want 3", e3.Seq)
	}
	from2, err := w2.Read(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(from2) != 2 || from2[0].Seq != 2 {
		t.Fatalf("Read(2) = %d events starting at %d, want 2 starting at 2", len(from2), from2[0].Seq)
	}
}

func TestWALRejectsMissingTenant(t *testing.T) {
	w, err := OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if _, err := w.Append(context.Background(), Envelope{Type: "t"}); err == nil {
		t.Fatal("expected error for missing org/workspace, got nil")
	}
}

func TestSubscribeReceivesAppends(t *testing.T) {
	w, err := OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	ch, cancel := w.Subscribe()
	defer cancel()
	if _, err := w.Append(context.Background(), Envelope{Org: "o", Workspace: "w", Type: "t", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-ch:
		if e.Seq != 1 {
			t.Fatalf("subscribed seq = %d, want 1", e.Seq)
		}
	default:
		t.Fatal("expected event on subscription channel")
	}
}

// FuzzWAL feeds arbitrary bytes as an on-disk log and asserts the decoder is
// robust: OpenWAL + Read must never panic, and a successful Read must return a
// contiguous run of seqs (1..Head) — i.e. the load() offset index and the Read()
// record split never desync into a silent mis-read. Corruption is allowed to error
// loudly; it is not allowed to crash or to return wrong/garbled envelopes.
func FuzzWAL(f *testing.F) {
	f.Add([]byte(`{"org":"o","workspace":"w","type":"t"}` + "\n"))
	f.Add([]byte("\n\n{\"org\":\"o\",\"workspace\":\"w\",\"type\":\"t\"}\n{garbage"))
	f.Add([]byte(`{"org":"o","workspace":"w","type":"a"}` + "\n" + `{"org":"o","workspace":"w","type":"b"}` + "\n"))
	f.Add([]byte("   \n\x00\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "events.log"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		w, err := OpenWAL(dir)
		if err != nil {
			return // an unreadable/corrupt log may fail to open — loud, not a crash
		}
		defer func() { _ = w.Close() }()
		evs, err := w.Read(context.Background(), 1)
		if err != nil {
			return // a corrupt record fails loudly at Read — acceptable
		}
		if uint64(len(evs)) != w.Head() {
			t.Fatalf("Read returned %d events but Head()=%d", len(evs), w.Head())
		}
		for i, e := range evs {
			if e.Seq != uint64(i+1) {
				t.Fatalf("non-contiguous seq at index %d: got %d, want %d", i, e.Seq, i+1)
			}
		}
	})
}

// TestWALUniqueKeyConflict asserts the optimistic-concurrency claim: a second
// append carrying a Unique key already used returns ErrConflict, while a distinct
// key (or an empty one) is unconstrained.
func TestWALUniqueKeyConflict(t *testing.T) {
	ctx := context.Background()
	w, err := OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	base := Envelope{Org: "o", Workspace: "w", Type: "t"}

	claim := base
	claim.Unique = "flow.version\x00F\x001"
	if _, err := w.Append(ctx, claim); err != nil {
		t.Fatalf("first claim should succeed: %v", err)
	}
	if _, err := w.Append(ctx, claim); !errors.Is(err, ErrConflict) {
		t.Fatalf("second claim of the same key should be ErrConflict, got %v", err)
	}
	// A different claim and an unconstrained append both succeed.
	other := base
	other.Unique = "flow.version\x00F\x002"
	if _, err := w.Append(ctx, other); err != nil {
		t.Fatalf("distinct claim should succeed: %v", err)
	}
	if _, err := w.Append(ctx, base); err != nil {
		t.Fatalf("unconstrained append should succeed: %v", err)
	}
}
