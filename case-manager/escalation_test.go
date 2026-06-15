// SPDX-License-Identifier: AGPL-3.0-or-later

package casemanager_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/case-manager/cases"
	decisioncmd "github.com/e6qu/intraktible/decision-engine/command"
	decisiondomain "github.com/e6qu/intraktible/decision-engine/domain"
	decisionevents "github.com/e6qu/intraktible/decision-engine/events"
	decisionflows "github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

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
	res, err := decisioncmd.NewDecideHandler(log, rm).Decide(ctx, id, "escalate", "production", map[string]any{"company": "Acme Corp"}, decisioncmd.EntityRef{})
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
