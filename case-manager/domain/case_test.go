// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"testing"

	"github.com/e6qu/intraktible/case-manager/domain"
)

func TestRequestReviewValidate(t *testing.T) {
	if err := (domain.RequestReview{CompanyName: "Acme", CaseType: "aml", SLADays: 5}).Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	bad := []domain.RequestReview{
		{CaseType: "aml"},     // no company
		{CompanyName: "Acme"}, // no type
		{CompanyName: "Acme", CaseType: "aml", SLADays: -1}, // negative SLA
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Fatalf("case %d: expected error for %+v", i, c)
		}
	}
}

func TestLifecycleCommandValidate(t *testing.T) {
	if err := (domain.AssignCase{CaseID: "c1", Assignee: "u"}).Validate(); err != nil {
		t.Fatalf("valid assign rejected: %v", err)
	}
	if err := (domain.AssignCase{CaseID: "c1"}).Validate(); err == nil {
		t.Fatal("assign without assignee should be rejected")
	}
	if err := (domain.SetStatus{CaseID: "c1", Status: domain.StatusInProgress}).Validate(); err != nil {
		t.Fatalf("valid status rejected: %v", err)
	}
	if err := (domain.SetStatus{CaseID: "c1", Status: "bogus"}).Validate(); err == nil {
		t.Fatal("invalid status should be rejected")
	}
	if err := (domain.AddNote{CaseID: "c1", Text: "hi"}).Validate(); err != nil {
		t.Fatalf("valid note rejected: %v", err)
	}
	if err := (domain.AddNote{CaseID: "c1", Text: "  "}).Validate(); err == nil {
		t.Fatal("blank note should be rejected")
	}
}

func TestParseStatus(t *testing.T) {
	if s, ok := domain.ParseStatus("in_progress"); !ok || s != domain.StatusInProgress {
		t.Fatalf("ParseStatus(in_progress) = %q,%v", s, ok)
	}
	// An unknown status (a hand-crafted/legacy event) must be rejected at the
	// boundary so the projector never writes it into the read model.
	if _, ok := domain.ParseStatus("archived"); ok {
		t.Fatal("ParseStatus must reject an unknown status")
	}
	if _, ok := domain.ParseStatus(""); ok {
		t.Fatal("ParseStatus must reject an empty status")
	}
}
