// SPDX-License-Identifier: AGPL-3.0-or-later

package audit_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/audit"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
)

func appended(t *testing.T, log eventlog.Log, e eventlog.Envelope) {
	t.Helper()
	if _, err := log.Append(context.Background(), e); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// seed writes a small mixed trail for tenant o/w (plus one event for a different
// tenant) and returns the caller identity.
func seed(t *testing.T, log eventlog.Log) identity.Identity {
	t.Helper()
	base := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	appended(t, log, eventlog.Envelope{Org: "o", Workspace: "w", Actor: "alice", Stream: "flows", Type: "flow.created", Time: base, Payload: []byte(`{"flow_id":"f1"}`)})
	appended(t, log, eventlog.Envelope{Org: "o", Workspace: "w", Actor: "bob", Stream: "flows", Type: "flow.version_published", Time: base.Add(time.Hour), Payload: []byte(`{"flow_id":"f1","version":1}`)})
	appended(t, log, eventlog.Envelope{Org: "o", Workspace: "w", Actor: "alice", Stream: "cases", Type: "case.opened", Time: base.Add(2 * time.Hour), Payload: []byte(`{"case_id":"c9"}`)})
	// A different tenant's event must never leak into o/w's trail.
	appended(t, log, eventlog.Envelope{Org: "other", Workspace: "w", Actor: "mallory", Stream: "flows", Type: "flow.created", Time: base, Payload: []byte(`{"flow_id":"x"}`)})
	return identity.Identity{Org: "o", Workspace: "w", Actor: "alice"}
}

func TestReadScopesToTenantNewestFirst(t *testing.T) {
	log, _ := testutil.NewLogStore(t)
	id := seed(t, log)

	got, err := audit.Read(context.Background(), log, id, audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 tenant entries, got %d: %+v", len(got), got)
	}
	// Newest first: case.opened (seq 3) precedes the flow events.
	if got[0].Type != "case.opened" || got[0].Seq < got[1].Seq {
		t.Fatalf("not newest-first: %+v", got)
	}
	for _, e := range got {
		if e.Type == "flow.created" && e.Actor == "mallory" {
			t.Fatal("other tenant's event leaked into the trail")
		}
	}
}

func TestReadFilters(t *testing.T) {
	log, _ := testutil.NewLogStore(t)
	id := seed(t, log)
	ctx := context.Background()

	byStream, _ := audit.Read(ctx, log, id, audit.Query{Stream: "cases"})
	if len(byStream) != 1 || byStream[0].Stream != "cases" {
		t.Fatalf("stream filter: %+v", byStream)
	}
	byActor, _ := audit.Read(ctx, log, id, audit.Query{Actor: "bob"})
	if len(byActor) != 1 || byActor[0].Actor != "bob" {
		t.Fatalf("actor filter: %+v", byActor)
	}
	byType, _ := audit.Read(ctx, log, id, audit.Query{Type: "flow.created"})
	if len(byType) != 1 {
		t.Fatalf("type filter: %+v", byType)
	}
	// Resource filter walks the payload: both flow events reference f1, the case does not.
	byResource, _ := audit.Read(ctx, log, id, audit.Query{Resource: "f1"})
	if len(byResource) != 2 {
		t.Fatalf("resource filter f1: want 2, got %+v", byResource)
	}
}

func TestReadTimeRangeAndLimit(t *testing.T) {
	log, _ := testutil.NewLogStore(t)
	id := seed(t, log)
	ctx := context.Background()

	// Only events at/after 10:00 (the published + case events).
	since := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)
	windowed, _ := audit.Read(ctx, log, id, audit.Query{Since: since})
	if len(windowed) != 2 {
		t.Fatalf("since filter: want 2, got %+v", windowed)
	}

	limited, _ := audit.Read(ctx, log, id, audit.Query{Limit: 1})
	if len(limited) != 1 || limited[0].Type != "case.opened" {
		t.Fatalf("limit: want the single newest, got %+v", limited)
	}
}

func TestCSV(t *testing.T) {
	log, _ := testutil.NewLogStore(t)
	id := seed(t, log)
	entries, _ := audit.Read(context.Background(), log, id, audit.Query{})
	doc, err := audit.CSV(entries)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(doc), "\n")
	if len(lines) != 4 { // header + 3 entries
		t.Fatalf("want header + 3 rows, got %d:\n%s", len(lines), doc)
	}
	if !strings.HasPrefix(lines[0], "seq,time,actor,stream,type,payload") {
		t.Fatalf("unexpected header: %q", lines[0])
	}
	if !strings.Contains(doc, "case.opened") || !strings.Contains(doc, "alice") {
		t.Fatalf("export missing expected content:\n%s", doc)
	}
}
