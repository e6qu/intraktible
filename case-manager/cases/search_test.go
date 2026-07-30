// SPDX-License-Identifier: AGPL-3.0-or-later

package cases_test

import (
	"testing"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/domain"
)

func TestFindDuplicatesUsesStrongestDeterministicKey(t *testing.T) {
	views := []cases.CaseView{
		{CaseID: "c2", Subject: "company/acme", CompanyName: "Acme", CaseType: "aml", Status: domain.StatusNeedsReview},
		{CaseID: "c1", Subject: "company/acme", CompanyName: "Acme", CaseType: "aml", Status: domain.StatusInProgress},
		{CaseID: "c3", CompanyName: "Beta", CaseType: "kyc", Status: domain.StatusNeedsReview},
		{CaseID: "c4", CompanyName: " beta ", CaseType: "kyc", Status: domain.StatusNeedsReview},
		{CaseID: "closed", CompanyName: "Beta", CaseType: "kyc", Status: domain.StatusCompleted},
	}
	groups := cases.FindDuplicates(views)
	if len(groups) != 2 {
		t.Fatalf("duplicate groups = %+v", groups)
	}
	if groups[0].Key == groups[1].Key ||
		groups[0].Key[:len("duplicate:")] != "duplicate:" ||
		groups[1].Key[:len("duplicate:")] != "duplicate:" {
		t.Fatalf("duplicate keys are not distinct opaque identifiers: %+v", groups)
	}
	var open cases.DuplicateGroup
	for _, group := range groups {
		if group.Reason == "same open case type and company" {
			open = group
		}
	}
	if len(open.CaseIDs) != 2 || open.CaseIDs[0] != "c3" || open.CaseIDs[1] != "c4" {
		t.Fatalf("open duplicate group = %+v", open)
	}
}
