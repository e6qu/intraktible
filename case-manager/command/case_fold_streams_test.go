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

// The SLA fold reads two streams, and the reason is easy to lose.
//
// It used to read the entire log, which made the stream question invisible.
// Narrowing it to the cases stream alone looks obviously right -- every type
// it handles is named cases.* -- and is wrong: a case can be opened by the
// decision engine's ManualReviewRequested, which lives on the DECISIONS
// stream, and suspend/resume arrive there too. That narrowing compiles, reads
// cleanly, and makes every escalated case vanish.
//
// This test fails if someone drops the decisions half again.
func TestCaseFoldSeesCasesOpenedOnTheDecisionsStream(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	h := command.NewHandler(log)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}

	// Interleave a high-volume unrelated stream around both halves, the shape
	// that made the original full-log read so expensive.
	noise := func() {
		if _, err := log.Append(ctx, eventlog.Envelope{
			Org: id.Org, Workspace: id.Workspace, Actor: "sys",
			Stream: "platform.leader", Type: "platform.leader.claimed",
			Time: time.Now().UTC(), Payload: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}

	noise()
	payload, err := json.Marshal(decisionevents.ManualReviewRequested{
		CaseID: "esc-stream", DecisionID: "d1", NodeID: "n1",
		CompanyName: "Acme", CaseType: "aml", SLADays: 30,
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
	noise()

	// Acting on the case proves the fold saw its opening event: the existence
	// check rejects an unknown case.
	if _, err := h.AssignCase(ctx, id, domain.AssignCase{CaseID: "esc-stream", Assignee: "rev"}); err != nil {
		t.Fatalf("case opened on the decisions stream was invisible to the fold: %v", err)
	}
	if _, err := h.SetStatus(ctx, id, domain.SetStatus{
		CaseID: "esc-stream", Status: domain.StatusInProgress,
	}); err != nil {
		t.Fatalf("status change on a decision-opened case: %v", err)
	}
}

// The other half: a manually opened case must still be seen, and the two
// streams must interleave by Seq rather than one being appended after the
// other -- the fold carries state forward in sequence order.
func TestCaseFoldSeesManuallyOpenedCasesAndKeepsSeqOrder(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	h := command.NewHandler(log)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}

	manualID, _, err := h.RequestReview(ctx, id, domain.RequestReview{
		CompanyName: "Acme", CaseType: "aml", SLADays: 30,
	})
	if err != nil {
		t.Fatalf("request review: %v", err)
	}
	if _, err := h.AssignCase(ctx, id, domain.AssignCase{CaseID: manualID, Assignee: "rev"}); err != nil {
		t.Fatalf("assign manually opened case: %v", err)
	}

	// A decisions-stream event appended AFTER the cases events must still fold
	// in its true position; a naive concatenation would misorder the two.
	payload, marshalErr := json.Marshal(decisionevents.ManualReviewRequested{
		CaseID: "esc-2", DecisionID: "d2", NodeID: "n1",
		CompanyName: "Beta", CaseType: "aml", SLADays: 30,
	})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if _, err := log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: "engine",
		Stream: decisionevents.StreamDecisions, Type: decisionevents.TypeManualReviewRequested,
		Time: time.Now().UTC(), Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.AssignCase(ctx, id, domain.AssignCase{CaseID: "esc-2", Assignee: "rev"}); err != nil {
		t.Fatalf("assign case opened after the manual one: %v", err)
	}

	// And an unknown case is still rejected, so the fold has not simply become
	// permissive.
	if _, err := h.AssignCase(ctx, id, domain.AssignCase{CaseID: "nope", Assignee: "rev"}); err == nil {
		t.Fatal("assigning an unknown case succeeded")
	}
}
