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
	doc := reconsideration.Explain(rec, nil, nil, time.Now()) // nil regimes = all apply

	for _, want := range []string{
		"How this decision was made",
		"solely automated means",
		"Article 22 of the EU General Data Protection Regulation",
		"Articles 22A to 22D of the UK Data (Use and Access) Act 2025",
		"US Equal Credit Opportunity Act",
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

func TestExplainCitesOnlyApplicableRegimes(t *testing.T) {
	rec := history.Record{DecisionID: "dec-1", Status: "completed", Disposition: string(policy.Decline)}

	// A UK-only workspace cites the UK Act and not the EU Regulation.
	uk := reconsideration.Explain(rec, nil, []string{"uk"}, time.Now())
	if strings.Contains(uk, "EU General Data Protection Regulation") {
		t.Errorf("a UK-only explanation should not cite the EU Regulation:\n%s", uk)
	}
	if !strings.Contains(uk, "UK Data (Use and Access) Act 2025") {
		t.Errorf("a UK explanation should cite the UK Act:\n%s", uk)
	}

	// A US-only workspace cites the Equal Credit Opportunity Act, not Article 22.
	us := reconsideration.Explain(rec, nil, []string{"us"}, time.Now())
	if strings.Contains(us, "Article 22") {
		t.Errorf("a US-only explanation should not cite Article 22:\n%s", us)
	}
	if !strings.Contains(us, "US Equal Credit Opportunity Act") {
		t.Errorf("a US explanation should cite the ECOA:\n%s", us)
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
	doc := reconsideration.Explain(rec, review, nil, time.Now())

	if strings.Contains(doc, "solely automated means") {
		t.Error("a human-involved decision must not be described as solely automated")
	}
	if !strings.Contains(doc, "overturned") || !strings.Contains(doc, "Paystub the pull missed") {
		t.Fatalf("explanation should reflect the human review:\n%s", doc)
	}
}
