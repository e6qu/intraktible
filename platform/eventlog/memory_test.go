// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestMemoryAppendReadHead(t *testing.T) {
	ctx := context.Background()
	l := NewMemory()
	defer func() { _ = l.Close() }()

	e1, err := l.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "t", Payload: json.RawMessage(`{"n":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if e1.Seq != 1 {
		t.Fatalf("seq = %d, want 1", e1.Seq)
	}
	if e1.ID == "" {
		t.Fatal("append must stamp a missing ID")
	}
	if _, err := l.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "t", Payload: json.RawMessage(`{"n":2}`)}); err != nil {
		t.Fatal(err)
	}
	if l.Head() != 2 {
		t.Fatalf("head = %d, want 2", l.Head())
	}
	got, err := l.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("Read(0) = %+v, want seqs 1,2", got)
	}
	from2, err := l.Read(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(from2) != 1 || from2[0].Seq != 2 {
		t.Fatalf("Read(2) = %+v, want 1 event at seq 2", from2)
	}
	past, err := l.Read(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if past != nil {
		t.Fatalf("Read past head = %+v, want nil", past)
	}
}

// TestMemoryReadIsASnapshot: a slice Read returned must not grow or change when
// the log is appended to afterwards.
func TestMemoryReadIsASnapshot(t *testing.T) {
	ctx := context.Background()
	l := NewMemory()
	defer func() { _ = l.Close() }()
	if _, err := l.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "a"}); err != nil {
		t.Fatal(err)
	}
	snap, err := l.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "b"}); err != nil {
		t.Fatal(err)
	}
	if len(snap) != 1 || snap[0].Type != "a" {
		t.Fatalf("snapshot changed after append: %+v", snap)
	}
}

func TestMemoryRejectsMissingTenant(t *testing.T) {
	l := NewMemory()
	defer func() { _ = l.Close() }()
	if _, err := l.Append(context.Background(), Envelope{Type: "t"}); err == nil {
		t.Fatal("expected error for missing org/workspace, got nil")
	}
}

func TestMemorySubscribeReceivesAppends(t *testing.T) {
	l := NewMemory()
	defer func() { _ = l.Close() }()
	ch, cancel := l.Subscribe()
	defer cancel()
	if _, err := l.Append(context.Background(), Envelope{Org: "o", Workspace: "w", Type: "t", Payload: json.RawMessage(`{}`)}); err != nil {
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
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel must be closed after cancel")
	}
}

func TestMemoryUniqueKeyConflict(t *testing.T) {
	ctx := context.Background()
	l := NewMemory()
	defer func() { _ = l.Close() }()
	base := Envelope{Org: "o", Workspace: "w", Type: "t"}

	claim := base
	claim.Unique = "flow.version\x00F\x001"
	if _, err := l.Append(ctx, claim); err != nil {
		t.Fatalf("first claim should succeed: %v", err)
	}
	if _, err := l.Append(ctx, claim); !errors.Is(err, ErrConflict) {
		t.Fatalf("second claim of the same key should be ErrConflict, got %v", err)
	}
	other := base
	other.Unique = "flow.version\x00F\x002"
	if _, err := l.Append(ctx, other); err != nil {
		t.Fatalf("distinct claim should succeed: %v", err)
	}
	if _, err := l.Append(ctx, base); err != nil {
		t.Fatalf("unconstrained append should succeed: %v", err)
	}
}

// TestMemoryConcurrentAppends drives parallel appenders and asserts every event
// got a distinct, contiguous seq (run with -race).
func TestMemoryConcurrentAppends(t *testing.T) {
	ctx := context.Background()
	l := NewMemory()
	defer func() { _ = l.Close() }()
	const n = 100
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := l.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: fmt.Sprintf("t%d", i)}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if l.Head() != n {
		t.Fatalf("head = %d, want %d", l.Head(), n)
	}
	evs, err := l.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != n {
		t.Fatalf("read %d events, want %d", len(evs), n)
	}
	for i, e := range evs {
		if e.Seq != uint64(i+1) {
			t.Fatalf("non-contiguous seq at index %d: got %d, want %d", i, e.Seq, i+1)
		}
	}
}

func TestMemoryClosed(t *testing.T) {
	ctx := context.Background()
	l := NewMemory()
	ch, cancel := l.Subscribe()
	defer cancel()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "t"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("append after close = %v, want ErrClosed", err)
	}
	if _, err := l.Read(ctx, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("read after close = %v, want ErrClosed", err)
	}
	if _, ok := <-ch; ok {
		t.Fatal("subscription must be closed by Close")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("double close: %v", err)
	}
}

// TestMemoryExportRoundTrip: Export + NewMemoryFrom restores head, contents, and
// the Unique claims; new appends continue at the next seq; and the exported
// slice is a copy, not a window into the live log.
func TestMemoryExportRoundTrip(t *testing.T) {
	ctx := context.Background()
	l := NewMemory()
	defer func() { _ = l.Close() }()
	claim := Envelope{Org: "o", Workspace: "w", Type: "a", Unique: "slug\x00s1"}
	if _, err := l.Append(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "b", Payload: json.RawMessage(`{"n":2}`)}); err != nil {
		t.Fatal(err)
	}

	exported := l.Export()
	if len(exported) != 2 {
		t.Fatalf("exported %d events, want 2", len(exported))
	}
	exported2 := l.Export()
	exported2[0].Type = "mutated"
	if fresh, _ := l.Read(ctx, 1); fresh[0].Type != "a" {
		t.Fatal("Export must return a copy; mutating it changed the log")
	}

	r, err := NewMemoryFrom(exported)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if r.Head() != 2 {
		t.Fatalf("rebuilt head = %d, want 2", r.Head())
	}
	got, err := r.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Type != "a" || got[1].Type != "b" || got[1].Seq != 2 {
		t.Fatalf("rebuilt log = %+v", got)
	}
	if _, err := r.Append(ctx, claim); !errors.Is(err, ErrConflict) {
		t.Fatalf("rebuilt log must restore Unique claims, got %v", err)
	}
	e3, err := r.Append(ctx, Envelope{Org: "o", Workspace: "w", Type: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if e3.Seq != 3 {
		t.Fatalf("seq after rebuild = %d, want 3", e3.Seq)
	}
}

func TestNewMemoryFromRejectsBadHistory(t *testing.T) {
	base := Envelope{Org: "o", Workspace: "w", Type: "t"}
	at := func(seq uint64) Envelope {
		e := base
		e.Seq = seq
		return e
	}
	for name, history := range map[string][]Envelope{
		"gap":        {at(1), at(3)},
		"disorder":   {at(2), at(1)},
		"not-from-1": {at(2), at(3)},
		"zero-seq":   {at(0)},
		"duplicate":  {at(1), at(1)},
	} {
		if _, err := NewMemoryFrom(history); err == nil {
			t.Errorf("%s: NewMemoryFrom accepted a non-contiguous history", name)
		}
	}
	l, err := NewMemoryFrom(nil)
	if err != nil {
		t.Fatalf("empty history must be valid: %v", err)
	}
	defer func() { _ = l.Close() }()
	if l.Head() != 0 {
		t.Fatalf("head of empty history = %d, want 0", l.Head())
	}
}
