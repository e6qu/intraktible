// SPDX-License-Identifier: AGPL-3.0-or-later

package command_test

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/command"
	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestSweepSLABreachesOverdueOnce(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	h := command.NewHandler(log)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}

	// A case due immediately (sla_days 0) and one with a long window.
	overdue, _, err := h.RequestReview(ctx, id, domain.RequestReview{CompanyName: "Acme", CaseType: "aml", SLADays: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.RequestReview(ctx, id, domain.RequestReview{CompanyName: "Globex", CaseType: "kyc", SLADays: 30}); err != nil {
		t.Fatal(err)
	}

	// Sweeping later than the deadline breaches only the overdue case.
	now := time.Now().UTC().Add(time.Hour)
	breached, err := h.SweepSLA(ctx, id, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(breached) != 1 || breached[0] != overdue {
		t.Fatalf("breached = %v, want [%s]", breached, overdue)
	}

	// A second sweep is idempotent — the case is already breached.
	again, err := h.SweepSLA(ctx, id, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("second sweep re-breached: %v", again)
	}
}

func TestSweepSLASkipsCompleted(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	h := command.NewHandler(log)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}

	caseID, _, err := h.RequestReview(ctx, id, domain.RequestReview{CompanyName: "Acme", CaseType: "aml", SLADays: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.SetStatus(ctx, id, domain.SetStatus{CaseID: caseID, Status: domain.StatusCompleted}); err != nil {
		t.Fatal(err)
	}
	breached, err := h.SweepSLA(ctx, id, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(breached) != 0 {
		t.Fatalf("a completed case should not breach: %v", breached)
	}
}
