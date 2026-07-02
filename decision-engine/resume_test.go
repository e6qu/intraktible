// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

// suspendGraph pauses at a durable human task, then runs a pass-through
// manual_review before the output — the shape that exercises both the resume
// merge and the post-resume escalation path.
func suspendGraph() events.Graph {
	cfg := func(s string) json.RawMessage { return json.RawMessage(s) }
	return events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "gate", Type: events.NodeManualReview, Config: cfg(`{"company_name":"'Acme Corp'","case_type":"'aml'","suspend":true,"output_key":"review"}`)},
			{ID: "second", Type: events.NodeManualReview, Config: cfg(`{"company_name":"'Acme Corp'","case_type":"'kyb'","sla_days":2}`)},
			{ID: "out", Type: events.NodeOutput, Config: cfg(`{"fields":["decision","review","predict","features","reason_codes"]}`)},
		},
		Edges: []events.Edge{{From: "in", To: "gate"}, {From: "gate", To: "second"}, {From: "second", To: "out"}},
	}
}

// suspendDecision publishes the suspend flow, decides to the pause, and returns a
// handler whose store carries the flow + history read models.
func suspendDecision(t *testing.T, ctx context.Context, id identity.Identity) (*command.DecideHandler, eventlog.Log, string) {
	t.Helper()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	st := store.NewMemory()
	publishFlow(t, ctx, log, st, id, "htask", "HumanTask", suspendGraph())

	dh := command.NewDecideHandler(log, st)
	res, err := dh.Decide(ctx, id, "htask", "sandbox", map[string]any{"applicant": "a1"}, command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StatusSuspended {
		t.Fatalf("status=%s err=%s, want suspended", res.Status, res.Error)
	}
	if err := projection.New(log, st, history.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	return dh, log, res.DecisionID
}

// TestResumeStripsReservedNamespaces proves a reviewer outcome cannot forge the
// engine-owned namespaces or overwrite the accumulated compliance trail — the same
// invariant the decide path enforces on caller input.
func TestResumeStripsReservedNamespaces(t *testing.T) {
	ctx := context.Background()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "reviewer"}
	dh, _, decisionID := suspendDecision(t, ctx, id)

	res, err := dh.ResumeDecision(ctx, id, decisionID, map[string]any{
		"decision":     "approve",
		"predict":      map[string]any{"pd": map[string]any{"probability": 0.01}},
		"features":     map[string]any{"txn_count_24h": 999},
		"reason_codes": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StatusCompleted {
		t.Fatalf("status=%s err=%s, want completed", res.Status, res.Error)
	}
	if res.Output["decision"] != "approve" {
		t.Fatalf("legit outcome field lost: %+v", res.Output)
	}
	// The output node materializes requested fields as nil when absent from the
	// record — forged means a non-nil value survived the strip.
	for _, k := range []string{"predict", "features"} {
		if v := res.Output[k]; v != nil {
			t.Fatalf("reviewer outcome forged engine-owned namespace %q: %+v", k, res.Output)
		}
	}
	if v, ok := res.Output["reason_codes"].([]any); ok && len(v) == 0 {
		t.Fatalf("reviewer outcome wiped reason_codes: %+v", res.Output)
	}
	if review, ok := res.Output["review"].(map[string]any); ok {
		if _, forged := review["predict"]; forged {
			t.Fatalf("forged namespace survived under the output key: %+v", review)
		}
	}
}

// TestResumeIsSingleShot proves one suspension resumes exactly once: the second
// resume errors (claim conflict or already-terminal), and the log carries exactly
// one DecisionResumed event.
func TestResumeIsSingleShot(t *testing.T) {
	ctx := context.Background()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "reviewer"}
	dh, log, decisionID := suspendDecision(t, ctx, id)

	if _, err := dh.ResumeDecision(ctx, id, decisionID, map[string]any{"decision": "approve"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dh.ResumeDecision(ctx, id, decisionID, map[string]any{"decision": "decline"}); err == nil {
		t.Fatal("second resume of the same suspension must fail")
	}
	evs, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	resumed := 0
	for _, e := range evs {
		if e.Type == events.TypeDecisionResumed {
			resumed++
		}
	}
	if resumed != 1 {
		t.Fatalf("DecisionResumed events = %d, want exactly 1", resumed)
	}
}

// TestResumeOpensDownstreamManualReviewCase proves a pass-through manual_review
// node executed after the pause still escalates (opens its case) when the resumed
// run completes, carrying the decision's recorded input as context.
func TestResumeOpensDownstreamManualReviewCase(t *testing.T) {
	ctx := context.Background()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "reviewer"}
	dh, log, decisionID := suspendDecision(t, ctx, id)

	res, err := dh.ResumeDecision(ctx, id, decisionID, map[string]any{"decision": "approve"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StatusCompleted {
		t.Fatalf("status=%s err=%s, want completed", res.Status, res.Error)
	}
	evs, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var opened []events.ManualReviewRequested
	for _, e := range evs {
		if e.Type != events.TypeManualReviewRequested {
			continue
		}
		var p events.ManualReviewRequested
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.DecisionID == decisionID && p.NodeID == "second" {
			opened = append(opened, p)
		}
	}
	if len(opened) != 1 {
		t.Fatalf("post-resume manual_review escalations = %d, want exactly 1", len(opened))
	}
	if !strings.Contains(string(opened[0].Context), "applicant") {
		t.Fatalf("escalation context should carry the recorded input, got: %s", opened[0].Context)
	}
}
