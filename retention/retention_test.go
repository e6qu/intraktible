// SPDX-License-Identifier: AGPL-3.0-or-later

package retention_test

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/intraktible/fairlending"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/retention"
)

var (
	ctx = context.Background()
	id  = identity.Identity{Org: "demo", Workspace: "main", Actor: "admin"}
)

// issuedAt seeds one adverse-action issuance for a subject, stamped at `at`.
func issuedAt(t *testing.T, at time.Time, subject string) store.Store {
	t.Helper()
	log := eventlog.NewMemory()
	h := fairlending.NewHandler(log).WithNow(func() time.Time { return at })
	e, err := h.Issue(ctx, id, fairlending.IssueCmd{
		DecisionID: "dec-1", Subject: subject, Method: fairlending.DeliveryMail,
		PrincipalReasons: []string{"DTI too high"}, ContentHash: "h", HashAlgo: "sha-256",
	})
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory()
	if err := (fairlending.IssuanceProjector{}).Apply(ctx, e, st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestRetainedWithinWindow(t *testing.T) {
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	// Issued 3 months ago — well inside the 25-month ECOA window.
	st := issuedAt(t, now.AddDate(0, -3, 0), "applicant/APP-1")

	retained, reason, err := retention.Retained(ctx, st, id, "applicant/APP-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if !retained {
		t.Fatal("a 3-month-old adverse-action record must still be retained")
	}
	if reason == "" {
		t.Fatal("a retained subject should carry a reason")
	}

	status, err := retention.StatusFor(ctx, st, id, "applicant/APP-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Retained || len(status.Items) != 1 || status.Items[0].Kind != "adverse_action" {
		t.Fatalf("status = %+v", status)
	}
	// A different subject is unaffected.
	if r, _, _ := retention.Retained(ctx, st, id, "applicant/OTHER", now); r {
		t.Fatal("an unrelated subject must not be retained")
	}
}

func TestNotRetainedAfterWindow(t *testing.T) {
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	// Issued 26 months ago — past the 25-month window.
	st := issuedAt(t, now.AddDate(0, -26, 0), "applicant/APP-1")

	retained, _, err := retention.Retained(ctx, st, id, "applicant/APP-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if retained {
		t.Fatal("a 26-month-old record has aged out of the 25-month window")
	}
}
