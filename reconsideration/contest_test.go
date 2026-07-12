// SPDX-License-Identifier: AGPL-3.0-or-later

package reconsideration_test

import (
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/reconsideration"
)

// applyContest folds every logged event through the ContestProjector into a store.
func applyContest(t *testing.T, log eventlog.Log) store.Store {
	t.Helper()
	st := store.NewMemory()
	evs, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if err := (reconsideration.ContestProjector{}).Apply(ctx, e, st); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestContestOpensThenResolves(t *testing.T) {
	log := eventlog.NewMemory()
	h := reconsideration.NewHandler(log).WithNow(func() time.Time { return now })

	if _, err := h.RecordContest(ctx, id, reconsideration.ContestCmd{
		DecisionID: "dec-1", Subject: "applicant/APP-1", Channel: reconsideration.ChannelPhone,
		Note: "Customer called disputing the decline.",
	}); err != nil {
		t.Fatal(err)
	}
	st := applyContest(t, log)
	c, ok, _ := reconsideration.ReadContest(ctx, st, id, "dec-1")
	if !ok || c.Resolved || c.Channel != reconsideration.ChannelPhone || c.ReceivedBy != "diego" {
		t.Fatalf("open contest = %+v", c)
	}
	if open, _ := reconsideration.ListContests(ctx, st, id); len(open) != 1 {
		t.Fatalf("expected 1 contest, got %d", len(open))
	}

	// Recording a human review of the same decision resolves the contest.
	if _, err := h.Record(ctx, id, reconsideration.RecordCmd{
		DecisionID: "dec-1", Basis: reconsideration.BasisApplicantContest,
		Outcome: reconsideration.OutcomeOverturned, Rationale: "Reviewed; the decline is reversed.",
	}); err != nil {
		t.Fatal(err)
	}
	st = applyContest(t, log)
	c, _, _ = reconsideration.ReadContest(ctx, st, id, "dec-1")
	if !c.Resolved || c.ResolvedAt == nil {
		t.Fatalf("contest should be resolved after a review: %+v", c)
	}
}

func TestReviewWithoutContestIsNoOp(t *testing.T) {
	log := eventlog.NewMemory()
	h := reconsideration.NewHandler(log).WithNow(func() time.Time { return now })
	// A proactive review with no prior contest must not create a contest record.
	if _, err := h.Record(ctx, id, reconsideration.RecordCmd{
		DecisionID: "dec-2", Basis: reconsideration.BasisProactive,
		Outcome: reconsideration.OutcomeUpheld, Rationale: "Confirmed on file.",
	}); err != nil {
		t.Fatal(err)
	}
	st := applyContest(t, log)
	if _, ok, _ := reconsideration.ReadContest(ctx, st, id, "dec-2"); ok {
		t.Fatal("a proactive review must not open a contest")
	}
}

func TestContestChannelValidation(t *testing.T) {
	h := reconsideration.NewHandler(eventlog.NewMemory()).WithNow(func() time.Time { return now })
	if _, err := h.RecordContest(ctx, id, reconsideration.ContestCmd{DecisionID: "d", Channel: "carrier_pigeon"}); err == nil {
		t.Error("an unknown contest channel should be rejected")
	}
	if _, err := h.RecordContest(ctx, id, reconsideration.ContestCmd{DecisionID: "  ", Channel: reconsideration.ChannelEmail}); err == nil {
		t.Error("an empty decision id should be rejected")
	}
}
