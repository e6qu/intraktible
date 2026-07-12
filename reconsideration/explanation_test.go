// SPDX-License-Identifier: AGPL-3.0-or-later

package reconsideration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/reconsideration"
)

func TestExplainSolelyAutomated(t *testing.T) {
	rec := history.Record{
		DecisionID: "dec-1", Status: "completed", Disposition: string(policy.Decline),
		DispositionReason: "Credit score below threshold",
		ReasonCodes: []history.ReasonCode{
			{Code: "LOW_SCORE", Description: "Credit score below threshold"},
			{Code: "DTI_HIGH", Description: "Debt-to-income ratio elevated"},
		},
		EndedAt: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC),
	}
	doc := reconsideration.Explain(rec, nil, time.Now())

	for _, want := range []string{
		"How this decision was made",
		"solely automated means",
		"Article 22",
		"decline",
		"Debt-to-income ratio elevated",
		"obtain human intervention",
		"contest",
		"No human review has been recorded",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("explanation missing %q:\n%s", want, doc)
		}
	}
}

func TestExplainReflectsHumanReview(t *testing.T) {
	rec := history.Record{
		DecisionID: "dec-2", Status: "completed", Disposition: string(policy.Decline),
		HumanReviewed: true, // a person resumed it — not solely automated
	}
	review := &reconsideration.Review{
		Outcome: reconsideration.OutcomeOverturned, Rationale: "Paystub the pull missed",
		ReviewedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}
	doc := reconsideration.Explain(rec, review, time.Now())

	if strings.Contains(doc, "solely automated means") {
		t.Error("a human-involved decision must not be described as solely automated")
	}
	if !strings.Contains(doc, "overturned") || !strings.Contains(doc, "Paystub the pull missed") {
		t.Fatalf("explanation should reflect the human review:\n%s", doc)
	}
}
