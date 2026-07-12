// SPDX-License-Identifier: AGPL-3.0-or-later

package reconsideration

import (
	"testing"

	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/policy"
)

func TestEligible(t *testing.T) {
	base := history.Record{DecisionID: "d", Status: "completed", Disposition: string(policy.Decline)}
	if err := eligible(base); err != nil {
		t.Fatalf("a completed automated decline should be eligible: %v", err)
	}

	notDone := base
	notDone.Status = "suspended"
	if eligible(notDone) == nil {
		t.Error("a non-completed decision is not eligible")
	}

	approved := base
	approved.Disposition = string(policy.Approve)
	if eligible(approved) == nil {
		t.Error("an approval has no adverse outcome to reconsider")
	}

	withCase := base
	withCase.CaseID = "case-9"
	if eligible(withCase) == nil {
		t.Error("a decision that routed to manual_review already had human review")
	}

	humanResumed := base
	humanResumed.HumanReviewed = true
	if eligible(humanResumed) == nil {
		t.Error("a human-resumed decision already had human review")
	}
}
