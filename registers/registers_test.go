// SPDX-License-Identifier: AGPL-3.0-or-later

package registers_test

import (
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/fairlending"
	"github.com/e6qu/intraktible/platform/consent"
	"github.com/e6qu/intraktible/reconsideration"
	"github.com/e6qu/intraktible/registers"
)

var at = time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)

func TestAdverseActionRegister(t *testing.T) {
	items := []fairlending.IssuanceView{{
		DecisionID: "dec-1", Subject: "applicant/APP-1", Method: fairlending.DeliveryMail,
		BasedOnConsumerReport: true, PrincipalReasons: []string{"DTI too high", "Thin file"},
		ContentHash: "9f86", IssuedAt: at, IssuedBy: "diego",
	}}
	csv, err := registers.AdverseActionCSV(items)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"decision_id", "dec-1", "applicant/APP-1", "2026-07-12", "mail", "DTI too high; Thin file", "diego"} {
		if !strings.Contains(csv, want) {
			t.Errorf("CSV missing %q:\n%s", want, csv)
		}
	}
	md := registers.AdverseActionMarkdown(items, "2026-07-12 09:00 UTC")
	if !strings.Contains(md, "# Adverse-action register") || !strings.Contains(md, "dec-1") {
		t.Fatalf("markdown:\n%s", md)
	}
	// Empty register renders a stated empty line, not a bare header.
	if empty := registers.AdverseActionMarkdown(nil, "now"); !strings.Contains(empty, "No adverse-action notices") {
		t.Fatalf("empty markdown:\n%s", empty)
	}
}

func TestReconsiderationRegister(t *testing.T) {
	items := []reconsideration.Review{{
		DecisionID: "dec-2", Subject: "applicant/APP-2", Basis: reconsideration.BasisApplicantContest,
		Outcome: reconsideration.OutcomeOverturned, Rationale: "Paystub missed", ReviewedAt: at, ReviewedBy: "lena",
	}}
	csv, err := registers.ReconsiderationCSV(items)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dec-2", "applicant_contest", "overturned", "Paystub missed", "lena"} {
		if !strings.Contains(csv, want) {
			t.Errorf("CSV missing %q:\n%s", want, csv)
		}
	}
}

func TestConsentRegister(t *testing.T) {
	items := []consent.Record{{
		Subject: "applicant/APP-3", Purpose: "credit_underwriting", Basis: consent.BasisContract,
		Granted: true, GrantedAt: &at, Evidence: &consent.Evidence{Method: consent.MethodESignature}, UpdatedBy: "diego",
	}}
	csv, err := registers.ConsentCSV(items)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"applicant/APP-3", "credit_underwriting", "contract", "active", "e_signature"} {
		if !strings.Contains(csv, want) {
			t.Errorf("CSV missing %q:\n%s", want, csv)
		}
	}
}

func TestCSVInjectionDefused(t *testing.T) {
	// A subject beginning with '=' must be quoted so a spreadsheet won't evaluate it.
	items := []consent.Record{{Subject: "=cmd()", Purpose: "p", Basis: consent.BasisConsent, Granted: true}}
	csv, err := registers.ConsentCSV(items)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(csv, "'=cmd()") {
		t.Fatalf("formula not defused:\n%s", csv)
	}
}
