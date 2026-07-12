// SPDX-License-Identifier: AGPL-3.0-or-later

package reconsideration_test

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/reconsideration"
)

var (
	ctx = context.Background()
	id  = identity.Identity{Org: "demo", Workspace: "main", Actor: "diego"}
	now = time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
)

func TestRecordAndRead(t *testing.T) {
	log := eventlog.NewMemory()
	st := store.NewMemory()
	h := reconsideration.NewHandler(log).WithNow(func() time.Time { return now })

	e, err := h.Record(ctx, id, reconsideration.RecordCmd{
		DecisionID: "dec-1", Subject: "applicant/APP-1",
		Basis: reconsideration.BasisApplicantContest, Outcome: reconsideration.OutcomeOverturned,
		Rationale: "Manual review of the paystubs shows income the automated pull missed; DTI is within policy.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := (reconsideration.Projector{}).Apply(ctx, e, st); err != nil {
		t.Fatal(err)
	}
	rv, found, err := reconsideration.Read(ctx, st, id, "dec-1")
	if err != nil || !found {
		t.Fatalf("review not found: %v", err)
	}
	if rv.Outcome != reconsideration.OutcomeOverturned || rv.Basis != reconsideration.BasisApplicantContest ||
		rv.ReviewedBy != "diego" || rv.Subject != "applicant/APP-1" {
		t.Fatalf("review = %+v", rv)
	}

	list, err := reconsideration.List(ctx, st, id)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v (%v)", list, err)
	}
}

func TestRecordValidation(t *testing.T) {
	h := reconsideration.NewHandler(eventlog.NewMemory()).WithNow(func() time.Time { return now })
	good := reconsideration.RecordCmd{
		DecisionID: "d", Basis: reconsideration.BasisProactive, Outcome: reconsideration.OutcomeUpheld, Rationale: "checked",
	}
	if _, err := h.Record(ctx, id, good); err != nil {
		t.Fatalf("valid review rejected: %v", err)
	}

	noDecision := good
	noDecision.DecisionID = "  "
	if _, err := h.Record(ctx, id, noDecision); err == nil {
		t.Error("empty decision_id should fail")
	}
	badBasis := good
	badBasis.Basis = "gut_feeling"
	if _, err := h.Record(ctx, id, badBasis); err == nil {
		t.Error("unknown basis should fail")
	}
	badOutcome := good
	badOutcome.Outcome = "maybe"
	if _, err := h.Record(ctx, id, badOutcome); err == nil {
		t.Error("unknown outcome should fail")
	}
	noRationale := good
	noRationale.Rationale = "   "
	if _, err := h.Record(ctx, id, noRationale); err == nil {
		t.Error("a review with no rationale (rubber stamp) should fail")
	}
}
