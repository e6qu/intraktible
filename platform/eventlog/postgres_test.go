// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/e6qu/intraktible/platform/eventlog"
)

// TestPostgresLog exercises the networked event log against a real PostgreSQL,
// pointed to by INTRAKTIBLE_TEST_POSTGRES (a pgx DSN). It is skipped otherwise —
// the default CI here has no Postgres (see BUGS.md D21). To run it:
//
//	docker compose -f deploy/docker-compose.yml --profile pg up -d postgres
//	INTRAKTIBLE_TEST_POSTGRES='postgres://postgres:intraktible@localhost:5432/intraktible' go test ./platform/eventlog/
func TestPostgresLog(t *testing.T) {
	dsn := os.Getenv("INTRAKTIBLE_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set INTRAKTIBLE_TEST_POSTGRES (a pgx DSN) to run the Postgres log test")
	}
	ctx := context.Background()

	// Start from a clean log so sequence numbers are deterministic.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS events`); err != nil {
		t.Fatal(err)
	}
	pool.Close()

	// Two logs over the same database stand in for two HA nodes.
	node1, err := eventlog.OpenPostgresLog(ctx, dsn, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = node1.Close() }()
	node2, err := eventlog.OpenPostgresLog(ctx, dsn, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = node2.Close() }()

	// node2 subscribes before any append; the poller should deliver every event,
	// including those appended by node1 — the networked-delivery guarantee.
	ch, cancel := node2.Subscribe()
	defer cancel()

	id := func(seq int) eventlog.Envelope {
		return eventlog.Envelope{Org: "o", Workspace: "w", Actor: "a", Stream: "s", Type: "evt", Time: time.Unix(int64(seq), 0).UTC()}
	}
	first, err := node1.Append(ctx, id(1))
	if err != nil || first.Seq != 1 {
		t.Fatalf("append 1 -> seq=%d err=%v", first.Seq, err)
	}
	second, err := node1.Append(ctx, id(2))
	if err != nil || second.Seq != 2 {
		t.Fatalf("append 2 -> seq=%d err=%v", second.Seq, err)
	}

	// Read on the other node is immediately consistent and ordered.
	got, err := node2.Read(ctx, 0)
	if err != nil || len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("node2 Read = %+v err=%v", got, err)
	}
	if h := node2.Head(); h != 2 {
		t.Fatalf("node2 Head = %d, want 2", h)
	}

	// A third append also reaches node2's subscriber via the poller.
	if _, err := node1.Append(ctx, id(3)); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	var seqs []uint64
	for len(seqs) < 3 {
		select {
		case e := <-ch:
			seqs = append(seqs, e.Seq)
		case <-deadline:
			t.Fatalf("node2 only received %v over the bus, want 3 events", seqs)
		}
	}
	for i, s := range seqs {
		if s != uint64(i+1) {
			t.Fatalf("delivered seqs = %v, want ordered 1..3", seqs)
		}
	}

	// A closed log refuses further appends.
	_ = node1.Close()
	if _, err := node1.Append(ctx, id(4)); err == nil {
		t.Fatal("append after close should fail")
	}
}

// TestPostgresLogNotifyFastPath proves the LISTEN/NOTIFY path delivers without
// waiting for the poll: both logs use a 30s poll, so a sub-5s cross-node delivery
// can only have come from a notification.
func TestPostgresLogNotifyFastPath(t *testing.T) {
	dsn := os.Getenv("INTRAKTIBLE_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set INTRAKTIBLE_TEST_POSTGRES (a pgx DSN) to run the Postgres log test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS events`); err != nil {
		t.Fatal(err)
	}
	pool.Close()

	writer, err := eventlog.OpenPostgresLog(ctx, dsn, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	reader, err := eventlog.OpenPostgresLog(ctx, dsn, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()

	ch, cancel := reader.Subscribe()
	defer cancel()

	if _, err := writer.Append(ctx, eventlog.Envelope{
		Org: "o", Workspace: "w", Actor: "a", Stream: "s", Type: "evt", Time: time.Unix(1, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-ch:
		if e.Seq != 1 {
			t.Fatalf("delivered seq %d, want 1", e.Seq)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("NOTIFY fast path did not deliver within 5s (poll interval is 30s)")
	}
}
