// SPDX-License-Identifier: AGPL-3.0-or-later

package history_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

var (
	idxCtx = context.Background()
	idxID  = identity.Identity{Org: "o", Workspace: "w"}
)

func apply(t *testing.T, st store.Store, seq uint64, typ string, payload any) {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	e := eventlog.Envelope{
		Org: idxID.Org, Workspace: idxID.Workspace, Seq: seq, Type: typ,
		Stream: events.StreamDecisions, Time: time.Unix(int64(seq), 0).UTC(), Payload: b,
	}
	if err := (history.Projector{}).Apply(idxCtx, e, st); err != nil {
		t.Fatal(err)
	}
}

// startDecision applies a DecisionStarted (creating the record + index entry).
func startDecision(t *testing.T, st store.Store, seq uint64, decisionID, slug, env, variant string) {
	apply(t, st, seq, events.TypeDecisionStarted, events.DecisionStarted{
		DecisionID: decisionID, FlowID: "f", Slug: slug, Version: 1, Environment: env, Variant: variant,
		Data: json.RawMessage(`{}`),
	})
}

func TestListPageIndexPaginatesAndOrders(t *testing.T) {
	st := store.NewMemory()
	// Five decisions, ascending seq (so ascending time); newest is d5.
	for i := 1; i <= 5; i++ {
		startDecision(t, st, uint64(i), decisionN(i), "credit", "production", "")
	}

	// Newest-first, first page of 2.
	page, err := history.ListPage(idxCtx, st, idxID, history.Filter{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 5 || len(page.Records) != 2 {
		t.Fatalf("page1 total=%d len=%d", page.Total, len(page.Records))
	}
	if page.Records[0].DecisionID != decisionN(5) || page.Records[1].DecisionID != decisionN(4) {
		t.Fatalf("page1 order = %s, %s", page.Records[0].DecisionID, page.Records[1].DecisionID)
	}
	// Second page.
	page2, err := history.ListPage(idxCtx, st, idxID, history.Filter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page2.Records[0].DecisionID != decisionN(3) || page2.Offset != 2 {
		t.Fatalf("page2 = %+v", page2.Records[0].DecisionID)
	}
	// Records are the FULL records (input carried through), not just index summaries.
	if len(page.Records[0].Data) == 0 {
		t.Fatal("ListPage must return full records (with Data), not index summaries")
	}
}

func TestListPageIndexFilters(t *testing.T) {
	st := store.NewMemory()
	startDecision(t, st, 1, "d1", "credit", "production", "champion")
	startDecision(t, st, 2, "d2", "fraud", "sandbox", "challenger")
	startDecision(t, st, 3, "d3", "credit", "sandbox", "")

	cases := []struct {
		name string
		f    history.Filter
		want int
	}{
		{"slug", history.Filter{Slug: "credit"}, 2},
		{"env", history.Filter{Environment: "sandbox"}, 2},
		{"variant", history.Filter{Variant: "challenger"}, 1},
		{"query", history.Filter{Query: "D2"}, 1},
		{"slug+env", history.Filter{Slug: "credit", Environment: "production"}, 1},
	}
	for _, c := range cases {
		page, err := history.ListPage(idxCtx, st, idxID, c.f)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if page.Total != c.want {
			t.Errorf("%s: total=%d want %d", c.name, page.Total, c.want)
		}
	}
}

func TestListPageIndexTracksStatusTransition(t *testing.T) {
	st := store.NewMemory()
	startDecision(t, st, 1, "d1", "credit", "production", "")
	// While only started, a status=completed filter finds nothing.
	if p, _ := history.ListPage(idxCtx, st, idxID, history.Filter{Status: "completed"}); p.Total != 0 {
		t.Fatalf("before completion: completed total=%d, want 0", p.Total)
	}
	// Complete it — the index entry's status must transition.
	apply(t, st, 2, events.TypeDecisionCompleted, events.DecisionCompleted{
		DecisionID: "d1", FlowID: "f", Version: 1, Output: json.RawMessage(`{}`),
	})
	if p, _ := history.ListPage(idxCtx, st, idxID, history.Filter{Status: "completed"}); p.Total != 1 {
		t.Fatalf("after completion: completed total=%d, want 1", p.Total)
	}
	if p, _ := history.ListPage(idxCtx, st, idxID, history.Filter{Status: "started"}); p.Total != 0 {
		t.Fatalf("after completion: started total=%d, want 0", p.Total)
	}
}

func decisionN(i int) string { return "d" + string(rune('0'+i)) }
