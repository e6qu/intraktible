// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !js

package eventlog_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
)

func event(stream, typ string) eventlog.Envelope {
	return eventlog.Envelope{
		Org: "demo", Workspace: "main", Actor: "dev",
		Stream: stream, Type: typ, Time: time.Unix(0, 0).UTC(),
		Payload: json.RawMessage(`{"k":1}`),
	}
}

func TestSQLiteLogReadTenantStream(t *testing.T) {
	ctx := context.Background()
	l, err := eventlog.OpenSQLiteLog(t.TempDir(), 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	ev := func(org, ws, stream, typ string) eventlog.Envelope {
		return eventlog.Envelope{
			Org: org, Workspace: ws, Actor: "d", Stream: stream, Type: typ,
			Time: time.Unix(0, 0).UTC(), Payload: json.RawMessage(`{}`),
		}
	}
	// Interleave tenants and streams so the filter is doing real work.
	for _, e := range []eventlog.Envelope{
		ev("o1", "w", "flows", "a"),
		ev("o1", "w", "decisions", "x"),
		ev("o2", "w", "flows", "b"),
		ev("o1", "w", "flows", "c"),
		ev("o1", "w2", "flows", "d"),
	} {
		if _, err := l.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	got, err := l.ReadTenantStream(ctx, "o1", "w", "flows", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Equivalence with a full read + manual filter (the contract the fallback-free
	// callers rely on).
	all, err := l.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var want []eventlog.Envelope
	for _, e := range all {
		if e.Org == "o1" && e.Workspace == "w" && e.Stream == "flows" {
			want = append(want, e)
		}
	}
	if len(got) != len(want) || len(got) != 2 {
		t.Fatalf("got %d events, want %d (2 for o1/w/flows)", len(got), len(want))
	}
	for i := range got {
		if got[i].Seq != want[i].Seq || got[i].Type != want[i].Type {
			t.Fatalf("mismatch at %d: got seq=%d type=%s, want seq=%d type=%s", i, got[i].Seq, got[i].Type, want[i].Seq, want[i].Type)
		}
	}
	// The fromSeq bound skips earlier matches.
	tail, err := l.ReadTenantStream(ctx, "o1", "w", "flows", got[0].Seq+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 1 || tail[0].Type != "c" {
		t.Fatalf("fromSeq bound: got %d events, want 1 (type c)", len(tail))
	}
}

func TestSQLiteLogAppendReadHead(t *testing.T) {
	ctx := context.Background()
	l, err := eventlog.OpenSQLiteLog(t.TempDir(), 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	for i := 0; i < 3; i++ {
		if _, err := l.Append(ctx, event("s", "t")); err != nil {
			t.Fatal(err)
		}
	}
	if l.Head() != 3 {
		t.Fatalf("head = %d, want 3", l.Head())
	}
	evs, err := l.Read(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Seq != 2 || evs[1].Seq != 3 {
		t.Fatalf("read from 2 = %+v", evs)
	}
}

// Two SQLiteLog handles on the same directory model two processes (the split
// profile): an append through one is visible to the other via Read, and the
// other's poller delivers it live to a subscriber. This is exactly what the file
// WAL could not do across processes (D18).
func TestSQLiteLogSharedAcrossHandles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	writer, err := eventlog.OpenSQLiteLog(dir, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()

	reader, err := eventlog.OpenSQLiteLog(dir, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()

	sub, cancel := reader.Subscribe()
	defer cancel()

	stored, err := writer.Append(ctx, event("orders", "created"))
	if err != nil {
		t.Fatal(err)
	}

	// The reader's Read sees the cross-handle append immediately.
	evs, err := reader.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Type != "created" {
		t.Fatalf("reader.Read = %+v", evs)
	}

	// And the reader's poller delivers it live to the subscriber.
	select {
	case got := <-sub:
		if got.Seq != stored.Seq || got.Type != "created" {
			t.Fatalf("subscriber got %+v, want seq %d", got, stored.Seq)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber never received the cross-handle event")
	}
}

// TestSQLiteUniqueKeyConflict proves the cross-process claim is enforced by the DB
// (the partial unique index), and that empty/NULL keys never collide.
func TestSQLiteUniqueKeyConflict(t *testing.T) {
	ctx := context.Background()
	l, err := eventlog.OpenSQLiteLog(t.TempDir(), 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	claim := event("flows", "flow.version.published")
	claim.Unique = "flow.version\x00F\x001"
	if _, err := l.Append(ctx, claim); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := l.Append(ctx, claim); !errors.Is(err, eventlog.ErrConflict) {
		t.Fatalf("duplicate claim should be ErrConflict, got %v", err)
	}
	// Two unconstrained (empty-key → NULL) appends must both succeed — NULL is
	// excluded from the partial unique index.
	for range 2 {
		if _, err := l.Append(ctx, event("flows", "flow.created")); err != nil {
			t.Fatalf("unconstrained append should succeed: %v", err)
		}
	}
}
