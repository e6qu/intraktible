// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import (
	"context"
	"encoding/json"
	"testing"
)

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
