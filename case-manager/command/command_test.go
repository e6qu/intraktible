// SPDX-License-Identifier: AGPL-3.0-or-later

package command_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/command"
	"github.com/e6qu/intraktible/case-manager/domain"
	decisionevents "github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestSweepSLABreachesOverdueOnce(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	h := command.NewHandler(log)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}

	// A case due immediately (sla_days 0) and one with a long window.
	overdue, _, err := h.RequestReview(ctx, id, domain.RequestReview{CompanyName: "Acme", CaseType: "aml", SLADays: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.RequestReview(ctx, id, domain.RequestReview{CompanyName: "Globex", CaseType: "kyc", SLADays: 30}); err != nil {
		t.Fatal(err)
	}

	// Sweeping later than the deadline breaches only the overdue case.
	now := time.Now().UTC().Add(time.Hour)
	breached, err := h.SweepSLA(ctx, id, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(breached) != 1 || breached[0] != overdue {
		t.Fatalf("breached = %v, want [%s]", breached, overdue)
	}

	// A second sweep is idempotent — the case is already breached.
	again, err := h.SweepSLA(ctx, id, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("second sweep re-breached: %v", again)
	}
}

// TestEscalatedCaseIsActionable guards the fix for decision-escalated cases:
// a case opened via the decision engine's ManualReviewRequested (not the manual
// ReviewRequested) must be assignable / status-changeable / notable, not rejected
// as "unknown" by the existence check.
func TestEscalatedCaseIsActionable(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	h := command.NewHandler(log)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}

	const caseID = "esc-1"
	payload, err := json.Marshal(decisionevents.ManualReviewRequested{
		CaseID: caseID, DecisionID: "d1", NodeID: "n1", CompanyName: "Acme", CaseType: "aml", SLADays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: "engine",
		Stream: decisionevents.StreamDecisions, Type: decisionevents.TypeManualReviewRequested,
		Time: time.Now().UTC(), Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := h.AssignCase(ctx, id, domain.AssignCase{CaseID: caseID, Assignee: "rev"}); err != nil {
		t.Fatalf("assign escalated case: %v", err)
	}
	if _, err := h.SetStatus(ctx, id, domain.SetStatus{CaseID: caseID, Status: domain.StatusInProgress}); err != nil {
		t.Fatalf("set status on escalated case: %v", err)
	}
	if _, err := h.AddNote(ctx, id, domain.AddNote{CaseID: caseID, Text: "looking into it"}); err != nil {
		t.Fatalf("add note to escalated case: %v", err)
	}

	// An unknown case is still rejected.
	if _, err := h.AssignCase(ctx, id, domain.AssignCase{CaseID: "nope", Assignee: "rev"}); err == nil {
		t.Fatal("assigning an unknown case should fail")
	}
}

func TestSweepSLASkipsCompleted(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	h := command.NewHandler(log)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}

	caseID, _, err := h.RequestReview(ctx, id, domain.RequestReview{CompanyName: "Acme", CaseType: "aml", SLADays: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.SetStatus(ctx, id, domain.SetStatus{CaseID: caseID, Status: domain.StatusCompleted}); err != nil {
		t.Fatal(err)
	}
	breached, err := h.SweepSLA(ctx, id, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(breached) != 0 {
		t.Fatalf("a completed case should not breach: %v", breached)
	}
}

// TestSetStatusRejectsReopeningCompleted guards the terminal-state transition
// rule: once a case is completed it cannot be moved back to an open status, which
// would silently re-arm the SLA sweep against a legitimately-closed case.
func TestSetStatusRejectsReopeningCompleted(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	h := command.NewHandler(log)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}

	caseID, _, err := h.RequestReview(ctx, id, domain.RequestReview{CompanyName: "Acme", CaseType: "aml", SLADays: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.SetStatus(ctx, id, domain.SetStatus{CaseID: caseID, Status: domain.StatusCompleted}); err != nil {
		t.Fatal(err)
	}
	// Reopening a completed case must be rejected.
	if _, err := h.SetStatus(ctx, id, domain.SetStatus{CaseID: caseID, Status: domain.StatusInProgress}); err == nil {
		t.Fatal("reopening a completed case should be rejected")
	}
	if _, err := h.SetStatus(ctx, id, domain.SetStatus{CaseID: caseID, Status: domain.StatusNeedsReview}); err == nil {
		t.Fatal("moving a completed case back to needs_review should be rejected")
	}
	// Re-asserting completed (idempotent no-op) is allowed.
	if _, err := h.SetStatus(ctx, id, domain.SetStatus{CaseID: caseID, Status: domain.StatusCompleted}); err != nil {
		t.Fatalf("re-asserting completed should be allowed: %v", err)
	}
}
