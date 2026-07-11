// SPDX-License-Identifier: AGPL-3.0-or-later

package command_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/command"
	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/case-manager/events"
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

	// A case with a 1-day window and one with a long window.
	overdue, _, err := h.RequestReview(ctx, id, domain.RequestReview{CompanyName: "Acme", CaseType: "aml", SLADays: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.RequestReview(ctx, id, domain.RequestReview{CompanyName: "Globex", CaseType: "kyc", SLADays: 30}); err != nil {
		t.Fatal(err)
	}

	// Sweeping two days later (past the 1-day deadline) breaches only the overdue case.
	now := time.Now().UTC().Add(48 * time.Hour)
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

// TestSweepSLADefaultsUnspecifiedWindow guards that a case opened with sla_days:0
// (unspecified) gets the default window in the SWEEPER too — so it is not breached the
// instant it opens, matching what the read model shows the reviewer.
func TestSweepSLADefaultsUnspecifiedWindow(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	h := command.NewHandler(log)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}

	if _, _, err := h.RequestReview(ctx, id, domain.RequestReview{CompanyName: "Acme", CaseType: "aml", SLADays: 0}); err != nil {
		t.Fatal(err)
	}
	// An hour after opening, a defaulted (3-day) case must NOT be overdue.
	breached, err := h.SweepSLA(ctx, id, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(breached) != 0 {
		t.Fatalf("a 0-day (defaulted) case must not breach an hour after opening: %v", breached)
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

// TestAssignIsAClaimNotABlindWrite: two reviewers who both open an unassigned case
// and both click Assign must not both be told they own it. The handlers are
// separate, as two nodes are — each has its own mutex, so only the log's claim can
// order them.
func TestAssignIsAClaimNotABlindWrite(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	alice := identity.Identity{Org: "demo", Workspace: "main", Actor: "alice"}
	bob := identity.Identity{Org: "demo", Workspace: "main", Actor: "bob"}
	nodeA, nodeB := command.NewHandler(log), command.NewHandler(log)

	caseID, _, err := nodeA.RequestReview(ctx, alice, domain.RequestReview{CompanyName: "Acme", CaseType: "aml", SLADays: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodeA.AssignCase(ctx, alice, domain.AssignCase{CaseID: caseID, Assignee: "alice"}); err != nil {
		t.Fatalf("claiming an unassigned case should succeed: %v", err)
	}
	// Bob claims the case Alice already owns: refused, and told who owns it.
	_, err = nodeB.AssignCase(ctx, bob, domain.AssignCase{CaseID: caseID, Assignee: "bob"})
	if err == nil {
		t.Fatal("claiming a case that is already assigned must fail")
	}
	if !strings.Contains(err.Error(), "alice") {
		t.Fatalf("the refusal should name the current assignee, got: %v", err)
	}
	// Re-claiming for the same assignee is a no-op the caller should hear about.
	if _, err := nodeB.AssignCase(ctx, bob, domain.AssignCase{CaseID: caseID, Assignee: "alice"}); err == nil {
		t.Fatal("re-assigning a case to its current assignee must fail")
	}
	// Taking it over is allowed, but only when asked for.
	if _, err := nodeB.AssignCase(ctx, bob, domain.AssignCase{CaseID: caseID, Assignee: "bob", Reassign: true}); err != nil {
		t.Fatalf("an explicit reassign should succeed: %v", err)
	}
	if _, err := nodeA.AssignCase(ctx, alice, domain.AssignCase{CaseID: caseID, Assignee: "alice", Reassign: true}); err != nil {
		t.Fatalf("reassigning back should succeed (the claim is per transition, not per pair): %v", err)
	}
}

// TestSetStatusIsAtomicAcrossProcesses: two nodes both fold `needs_review` inside
// the simultaneity window and each appends a transition. Their mutexes do not see
// each other, so without an expected-version claim both commit from the same folded
// state — and replaying the log then walks an illegal transition, reopening a
// completed case and re-arming its SLA sweep. With the claim, the loser re-folds and
// is judged against the status that actually won.
func TestSetStatusIsAtomicAcrossProcesses(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}
	nodeA, nodeB := command.NewHandler(log), command.NewHandler(log)

	caseID, _, err := nodeA.RequestReview(ctx, id, domain.RequestReview{CompanyName: "Acme", CaseType: "aml", SLADays: 3})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{}, 2)
	start := make(chan struct{})
	for _, tc := range []struct {
		node   *command.Handler
		status domain.CaseStatus
	}{{nodeA, domain.StatusCompleted}, {nodeB, domain.StatusInProgress}} {
		go func() {
			<-start
			_, _ = tc.node.SetStatus(ctx, id, domain.SetStatus{CaseID: caseID, Status: tc.status})
			done <- struct{}{}
		}()
	}
	close(start)
	for range 2 {
		<-done
	}

	// Every transition the log records must have been legal from the one before it.
	// Two commits from the same fold cannot satisfy that.
	evs, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	status := domain.StatusNeedsReview
	for _, e := range evs {
		if e.Type != events.TypeCaseStatusChanged {
			continue
		}
		var p events.CaseStatusChanged
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		next, valid := domain.ParseStatus(p.Status)
		if !valid {
			t.Fatalf("recorded status %q is not a status", p.Status)
		}
		if !status.CanTransitionTo(next) {
			t.Fatalf("the log records an illegal transition %s -> %s", status, next)
		}
		status = next
	}

	// And a case that reached the terminal status stays there.
	if status == domain.StatusCompleted {
		if _, err := nodeA.SetStatus(ctx, id, domain.SetStatus{CaseID: caseID, Status: domain.StatusInProgress}); err == nil {
			t.Fatal("a completed case must not reopen")
		}
	}
}

// TestAssignConcurrentClaimsElectOneWinner: N reviewers on N nodes all fold the
// same unassigned case and all append a claim. Only the log's claim can order them.
func TestAssignConcurrentClaimsElectOneWinner(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}

	caseID, _, err := command.NewHandler(log).RequestReview(ctx, id, domain.RequestReview{CompanyName: "Acme", CaseType: "aml", SLADays: 3})
	if err != nil {
		t.Fatal(err)
	}
	const reviewers = 4
	errs := make(chan error, reviewers)
	start := make(chan struct{})
	for i := range reviewers {
		node := command.NewHandler(log)
		go func() {
			<-start
			_, err := node.AssignCase(ctx, id, domain.AssignCase{CaseID: caseID, Assignee: "reviewer-" + strconv.Itoa(i)})
			errs <- err
		}()
	}
	close(start)
	claimed := 0
	for range reviewers {
		if <-errs == nil {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("exactly one reviewer must claim the case, got %d", claimed)
	}
}

// TestSweepSLADoesNotDoubleBreachAcrossProcesses: two schedulers (or a tick racing
// a manual sweep on another node) both fold a case as un-breached, then both append.
// The read model survives a duplicate, but the escalation hook and the notification
// webhook fire once per event — so the breach must be claimed once, globally.
func TestSweepSLADoesNotDoubleBreachAcrossProcesses(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}
	nodeA, nodeB := command.NewHandler(log), command.NewHandler(log)

	caseID, _, err := nodeA.RequestReview(ctx, id, domain.RequestReview{CompanyName: "Acme", CaseType: "aml", SLADays: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Sweep two days later, past the 1-day deadline.
	now := time.Now().UTC().Add(48 * time.Hour)

	results := make(chan []string, 2)
	start := make(chan struct{})
	for _, node := range []*command.Handler{nodeA, nodeB} {
		go func() {
			<-start
			breached, err := node.SweepSLA(ctx, id, now)
			if err != nil {
				t.Errorf("losing the breach claim is not an error, another node recorded it: %v", err)
			}
			results <- breached
		}()
	}
	close(start)
	reported := 0
	for range 2 {
		reported += len(<-results)
	}
	if reported != 1 {
		t.Fatalf("the breach was reported by %d sweeps, want exactly 1", reported)
	}

	evs, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var breaches int
	for _, e := range evs {
		if e.Type == events.TypeCaseSLABreached {
			breaches++
		}
	}
	if breaches != 1 {
		t.Fatalf("the SLA breach on case %s was recorded %d times, want exactly 1", caseID, breaches)
	}
}
