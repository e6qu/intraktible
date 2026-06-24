// SPDX-License-Identifier: AGPL-3.0-or-later

package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/command"
	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

// TestTickBreachesOverdueCasesPerTenant proves the scheduler records SLA breaches
// for every tenant's open-and-overdue cases (and only those) without the on-demand
// endpoint being hit, and is idempotent across ticks.
func TestTickBreachesOverdueCasesPerTenant(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()

	h := command.NewHandler(log)
	a := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}
	b := identity.Identity{Org: "other", Workspace: "main", Actor: "beth"}

	overdueA, _, err := h.RequestReview(ctx, a, domain.RequestReview{CompanyName: "Acme", CaseType: "aml", SLADays: 1})
	if err != nil {
		t.Fatal(err)
	}
	freshA, _, err := h.RequestReview(ctx, a, domain.RequestReview{CompanyName: "Beta", CaseType: "aml", SLADays: 30})
	if err != nil {
		t.Fatal(err)
	}
	overdueB, _, err := h.RequestReview(ctx, b, domain.RequestReview{CompanyName: "Gamma", CaseType: "kyb_kyc", SLADays: 1})
	if err != nil {
		t.Fatal(err)
	}

	st := store.NewMemory()
	if err := projection.New(log, st, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().AddDate(0, 0, 10) // well past the 1-day SLA
	s := &Scheduler{store: st, cmd: h, now: func() time.Time { return now }}

	sum, err := s.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Breached != 2 {
		t.Fatalf("breached = %d, want 2 (one overdue per tenant)", sum.Breached)
	}

	if err := projection.New(log, st, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id     identity.Identity
		caseID string
		want   bool
	}{
		{a, overdueA, true},
		{a, freshA, false},
		{b, overdueB, true},
	} {
		c, ok, err := cases.Read(ctx, st, tc.id, tc.caseID)
		if err != nil || !ok {
			t.Fatalf("read %s: ok=%v err=%v", tc.caseID, ok, err)
		}
		if c.SLABreached != tc.want {
			t.Fatalf("case %s SLABreached = %v, want %v", tc.caseID, c.SLABreached, tc.want)
		}
	}

	// A second tick is idempotent: already-breached cases are not re-emitted.
	sum, err = s.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Breached != 0 {
		t.Fatalf("second tick breached = %d, want 0 (idempotent)", sum.Breached)
	}
}
