// SPDX-License-Identifier: AGPL-3.0-or-later

package casemanager_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/case-manager/cases"
	casecmd "github.com/e6qu/intraktible/case-manager/command"
	casedomain "github.com/e6qu/intraktible/case-manager/domain"
	decisioncmd "github.com/e6qu/intraktible/decision-engine/command"
	decisiondomain "github.com/e6qu/intraktible/decision-engine/domain"
	decisionevents "github.com/e6qu/intraktible/decision-engine/events"
	decisionflows "github.com/e6qu/intraktible/decision-engine/flows"
	decisionhistory "github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/notifications"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func TestSuspendedDecisionOutcomeCompletesLinkedCaseAndTask(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "reviewer"}

	graph := decisionevents.Graph{
		Nodes: []decisionevents.Node{
			{ID: "in", Type: decisionevents.NodeInput},
			{ID: "review", Type: decisionevents.NodeManualReview, Config: json.RawMessage(
				`{"company_name":"'Acme'","case_type":"'underwriting'","suspend":true,"output_key":"review"}`,
			)},
			{ID: "out", Type: decisionevents.NodeOutput, Config: json.RawMessage(`{"fields":["review"]}`)},
		},
		Edges: []decisionevents.Edge{{From: "in", To: "review"}, {From: "review", To: "out"}},
	}
	fh := decisioncmd.NewHandler(log)
	flowID, _, err := fh.CreateFlow(ctx, id, decisiondomain.CreateFlow{Slug: "durable-review", Name: "Durable review"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fh.PublishVersion(ctx, id, decisiondomain.PublishVersion{FlowID: flowID, Graph: graph}); err != nil {
		t.Fatal(err)
	}

	rebuild := func() store.Store {
		st := store.NewMemory()
		if err := projection.New(log, st,
			decisionflows.Projector{}, decisionhistory.Projector{}, cases.Projector{}, notifications.Projector{},
		).Start(ctx); err != nil {
			t.Fatal(err)
		}
		return st
	}
	st := rebuild()
	dh := decisioncmd.NewDecideHandler(log, st)
	result, err := dh.Decide(ctx, id, "durable-review", "sandbox", map[string]any{"applicant": "A-1"}, decisioncmd.EntityRef{})
	if err != nil || result.Status != decisiondomain.StatusSuspended {
		t.Fatalf("decide: status=%s err=%v", result.Status, err)
	}

	st = rebuild()
	record, ok, err := decisionhistory.Read(ctx, st, id, result.DecisionID)
	if err != nil || !ok || record.CaseID == "" {
		t.Fatalf("suspended decision link: ok=%v record=%+v err=%v", ok, record, err)
	}
	reviewCase, ok, err := cases.Read(ctx, st, id, record.CaseID)
	if err != nil || !ok || reviewCase.SourceID != result.DecisionID {
		t.Fatalf("linked case: ok=%v case=%+v err=%v", ok, reviewCase, err)
	}

	// Generic case closure is misleading while the decision still needs the
	// approve/decline/refer outcome. The command boundary refuses it too.
	if _, err := casecmd.NewHandler(log).SetStatus(ctx, id, casedomain.SetStatus{
		CaseID: record.CaseID, Status: casedomain.StatusCompleted,
	}); err == nil {
		t.Fatal("suspended decision's case was completed without a review outcome")
	}

	if _, err := decisioncmd.NewDecideHandler(log, st).ResumeDecision(
		ctx, id, result.DecisionID, map[string]any{"decision": "approve"},
	); err != nil {
		t.Fatal(err)
	}
	st = rebuild()
	reviewCase, ok, err = cases.Read(ctx, st, id, record.CaseID)
	if err != nil || !ok || reviewCase.Status != casedomain.StatusCompleted {
		t.Fatalf("resumed case: ok=%v case=%+v err=%v", ok, reviewCase, err)
	}
	if got := reviewCase.Audit[len(reviewCase.Audit)-1].Type; got != "decision_resumed" {
		t.Fatalf("case terminal audit = %q, want decision_resumed", got)
	}
	inbox, err := notifications.List(ctx, st, id, notifications.Access{ReviewTasks: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range inbox {
		if item.SubjectType == "case" && item.SubjectID == record.CaseID {
			t.Fatalf("completed review task remained in the inbox: %+v", item)
		}
	}
}

// TestEscalationFromFlowOpensCase exercises the cross-component hook: a decision
// flow with a manual_review node escalates, and the Case Manager opens a case
// from the decision-engine event — wired only through the shared event log.
func TestEscalationFromFlowOpensCase(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "system"}

	// Publish a flow: input -> manual_review(company from data, aml, 7d) -> output.
	graph := decisionevents.Graph{
		Nodes: []decisionevents.Node{
			{ID: "in", Type: decisionevents.NodeInput},
			{ID: "mr", Type: decisionevents.NodeManualReview, Config: json.RawMessage(`{"company_name":"company","case_type":"'aml'","sla_days":7}`)},
			{ID: "out", Type: decisionevents.NodeOutput},
		},
		Edges: []decisionevents.Edge{{From: "in", To: "mr"}, {From: "mr", To: "out"}},
	}
	fh := decisioncmd.NewHandler(log)
	flowID, _, err := fh.CreateFlow(ctx, id, decisiondomain.CreateFlow{Slug: "escalate", Name: "Escalate"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fh.PublishVersion(ctx, id, decisiondomain.PublishVersion{FlowID: flowID, Graph: graph}); err != nil {
		t.Fatal(err)
	}

	// The decide path needs the flow registry read model.
	rm := store.NewMemory()
	if err := projection.New(log, rm, decisionflows.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := decisioncmd.NewDecideHandler(log, rm).Decide(ctx, id, "escalate", "sandbox", map[string]any{"company": "Acme Corp"}, decisioncmd.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != decisiondomain.StatusCompleted {
		t.Fatalf("decide status=%s err=%s", res.Status, res.Error)
	}

	// The Case Manager projector (over the same log) opens the case.
	cs := store.NewMemory()
	if err := projection.New(log, cs, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := cases.List(ctx, cs, id, cases.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 escalated case, got %d", len(list))
	}
	c := list[0]
	if c.CompanyName != "Acme Corp" || c.CaseType != "aml" || c.SLADays != 7 {
		t.Fatalf("case fields: %+v", c)
	}
	if c.Status != "needs_review" || c.SourceID != res.DecisionID {
		t.Fatalf("case should link to the decision: status=%s source=%s decision=%s", c.Status, c.SourceID, res.DecisionID)
	}
	if len(c.Audit) != 1 || c.Audit[0].Type != "requested" {
		t.Fatalf("audit: %+v", c.Audit)
	}
}

// TestEscalationLinksDecisionDefaultsSLAAndReasonCode exercises three alignments
// end-to-end on a manual_review escalation with NO sla_days: the recorded decision
// links to the case it opened (case_id), the case opens with the default SLA (3
// days, not immediately overdue), and the decision carries a MANUAL_REVIEW reason
// code even though the flow has no explicit Reason node.
func TestEscalationLinksDecisionDefaultsSLAAndReasonCode(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "system"}

	// manual_review with no sla_days -> the open path applies the default.
	graph := decisionevents.Graph{
		Nodes: []decisionevents.Node{
			{ID: "in", Type: decisionevents.NodeInput},
			{ID: "mr", Type: decisionevents.NodeManualReview, Config: json.RawMessage(`{"company_name":"'Acme Corp'","case_type":"'aml'"}`)},
			{ID: "out", Type: decisionevents.NodeOutput},
		},
		Edges: []decisionevents.Edge{{From: "in", To: "mr"}, {From: "mr", To: "out"}},
	}
	fh := decisioncmd.NewHandler(log)
	flowID, _, err := fh.CreateFlow(ctx, id, decisiondomain.CreateFlow{Slug: "escalate", Name: "Escalate"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fh.PublishVersion(ctx, id, decisiondomain.PublishVersion{FlowID: flowID, Graph: graph}); err != nil {
		t.Fatal(err)
	}

	rm := store.NewMemory()
	if err := projection.New(log, rm, decisionflows.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := decisioncmd.NewDecideHandler(log, rm).Decide(ctx, id, "escalate", "sandbox", map[string]any{}, decisioncmd.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != decisiondomain.StatusCompleted {
		t.Fatalf("decide status=%s err=%s", res.Status, res.Error)
	}

	// Item 4: the manual_review node contributes a MANUAL_REVIEW reason code.
	if rc, ok := res.Output["reason_codes"].([]any); !ok || len(rc) == 0 {
		t.Fatalf("expected a reason code in the output, got %v", res.Output["reason_codes"])
	} else if first, _ := rc[0].(map[string]any); first["code"] != "MANUAL_REVIEW" {
		t.Fatalf("expected MANUAL_REVIEW reason code, got %v", rc[0])
	}

	// Item 3: the case opens with the default SLA, not 0 (immediately overdue).
	cs := store.NewMemory()
	if err := projection.New(log, cs, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := cases.List(ctx, cs, id, cases.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 escalated case, got %d", len(list))
	}
	c := list[0]
	if c.SLADays != casedomain.DefaultSLADays {
		t.Fatalf("case SLADays = %d, want default %d", c.SLADays, casedomain.DefaultSLADays)
	}

	// Item 2: the recorded decision links to the case it opened.
	hist := store.NewMemory()
	if err := projection.New(log, hist, decisionhistory.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := decisionhistory.Read(ctx, hist, id, res.DecisionID)
	if err != nil || !ok {
		t.Fatalf("history read: ok=%v err=%v", ok, err)
	}
	if rec.CaseID != c.CaseID {
		t.Fatalf("decision case_id = %q, want %q", rec.CaseID, c.CaseID)
	}
	// The MANUAL_REVIEW code is also lifted onto the decision record's reason codes.
	if len(rec.ReasonCodes) == 0 || rec.ReasonCodes[0].Code != "MANUAL_REVIEW" {
		t.Fatalf("recorded reason codes = %+v, want a MANUAL_REVIEW code", rec.ReasonCodes)
	}
}
