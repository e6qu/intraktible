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

func TestListPageIndexSearchesTrackingDimensions(t *testing.T) {
	st := store.NewMemory()
	apply(t, st, 1, events.TypeDecisionStarted, events.DecisionStarted{
		DecisionID: "dec-1", FlowID: "f", Slug: "credit", Version: 1, Environment: "production",
		EntityType: "applicant", EntityID: "customer-77",
		BusinessReference: "application/ABC-123", CorrelationID: "trace-partner-9",
		Metadata: json.RawMessage(`{"channel":"branch","priority":2,"reviewed":true,"nested":{"region":"eu"}}`),
		Data:     json.RawMessage(`{}`),
	})

	cases := []history.Filter{
		{EntityType: "applicant", EntityID: "customer-77"},
		{BusinessReference: "application/ABC-123"},
		{CorrelationID: "trace-partner-9"},
		{Metadata: map[string]string{"channel": "branch"}},
		{Metadata: map[string]string{"priority": "2", "reviewed": "true"}},
		{Query: "abc-123"},
		{Query: "PARTNER-9"},
		{Query: "CUSTOMER-77"},
	}
	for _, filter := range cases {
		page, err := history.ListPage(idxCtx, st, idxID, filter)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 1 || page.Records[0].DecisionID != "dec-1" {
			t.Fatalf("filter %+v returned %+v", filter, page)
		}
	}
	for _, filter := range []history.Filter{
		{Metadata: map[string]string{"channel": "mobile"}},
		{Metadata: map[string]string{"nested": `{"region":"eu"}`}},
	} {
		page, err := history.ListPage(idxCtx, st, idxID, filter)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 0 {
			t.Fatalf("unsupported or mismatched metadata filter %+v returned %+v", filter, page)
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

func TestExecutionSummaryTracksRecoveryAndAttention(t *testing.T) {
	st := store.NewMemory()
	startDecision(t, st, 1, "running", "credit", "production", "")
	startDecision(t, st, 2, "recovering", "fraud", "production", "")
	apply(t, st, 3, events.TypeRecoveryClaimed, events.DecisionRecoveryClaimed{
		DecisionID: "recovering", Owner: "worker-a", Attempt: 2,
		LeaseUntil: time.Unix(100, 0).UTC(), PreviousErr: "worker lost",
	})
	startDecision(t, st, 4, "abandoned", "credit", "production", "")
	apply(t, st, 5, events.TypeRecoveryClaimed, events.DecisionRecoveryClaimed{
		DecisionID: "abandoned", Owner: "worker-b", Attempt: 3,
		LeaseUntil: time.Unix(101, 0).UTC(), PreviousErr: "provider timeout",
	})
	apply(t, st, 6, events.TypeDecisionAbandoned, events.DecisionAbandoned{
		DecisionID: "abandoned", FlowID: "f", Version: 1,
		Error: "provider outcome indeterminate", Attempt: 3,
	})
	startDecision(t, st, 7, "completed", "credit", "sandbox", "")
	apply(t, st, 8, events.TypeDecisionCompleted, events.DecisionCompleted{
		DecisionID: "completed", FlowID: "f", Version: 1, Output: json.RawMessage(`{}`),
	})

	summary, err := history.SummarizeExecution(idxCtx, st, idxID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 4 || summary.Running != 1 || summary.Retrying != 1 ||
		summary.Abandoned != 1 || summary.Completed != 1 || summary.RecoveryAttempts != 2 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(summary.Attention) != 3 ||
		summary.Attention[0].DecisionID != "abandoned" ||
		summary.Attention[0].LastRecoveryError != "provider outcome indeterminate" {
		t.Fatalf("attention = %+v", summary.Attention)
	}
}

func TestProjectorTracksEffectLifecycle(t *testing.T) {
	st := store.NewMemory()
	startDecision(t, st, 1, "d1", "credit", "production", "")
	apply(t, st, 2, events.TypeEffectRequested, events.DecisionEffectRequested{
		DecisionID: "d1", EffectID: "fx1", Scope: "live", FlowID: "f", Version: 1,
		NodeID: "bureau", Kind: "connect", Reference: "credit-bureau",
		Output: "report", InputHash: "abc", Attempt: 1,
	})
	apply(t, st, 3, events.TypeEffectSucceeded, events.DecisionEffectSucceeded{
		DecisionID: "d1", EffectID: "fx1", NodeID: "bureau", Kind: "connect",
		Attempt: 1, Output: json.RawMessage(`{"score":780}`), DurationMS: 12,
	})

	record, ok, err := history.Read(idxCtx, st, idxID, "d1")
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	if len(record.Effects) != 1 {
		t.Fatalf("effects = %+v", record.Effects)
	}
	effect := record.Effects[0]
	if effect.Status != "succeeded" || effect.Reference != "credit-bureau" ||
		effect.DurationMS != 12 || string(effect.Output) != `{"score":780}` {
		t.Fatalf("effect = %+v", effect)
	}
}

func decisionN(i int) string { return "d" + string(rune('0'+i)) }
