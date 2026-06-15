// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import (
	"context"
	"encoding/json"
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

// TestWALFailsOnMidFileCorruption confirms a corrupt complete record (not a torn
// tail) still fails loudly rather than being silently skipped.
func TestWALFailsOnMidFileCorruption(t *testing.T) {
	dir := t.TempDir()
	bad := "not valid json\n" + `{"org":"o","workspace":"w","type":"a","seq":2}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.log"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWAL(dir); err == nil {
		t.Fatal("a corrupt complete record should fail loudly")
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
