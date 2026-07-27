// SPDX-License-Identifier: AGPL-3.0-or-later

package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/command"
	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/case-manager/events"
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

// TestTickDeliversBreachWebhook proves the SLA escalation reaches the webhook
// channel, records its terminal outcome, and is not re-called on a later tick.
func TestTickDeliversBreachWebhook(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	h := command.NewHandler(log)
	a := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}
	overdue, _, err := h.RequestReview(ctx, a, domain.RequestReview{CompanyName: "Acme", CaseType: "aml", SLADays: 1})
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory()
	if err := projection.New(log, st, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().AddDate(0, 0, 10)
	var delivered []string
	s := (&Scheduler{store: st, cmd: h, now: func() time.Time { return now }}).WithNotify(
		func(_ context.Context, _ identity.Identity, caseID string) (DeliveryOutcome, error) {
			delivered = append(delivered, caseID)
			return DeliverySucceeded, nil
		})
	sum, err := s.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Delivered != 1 {
		t.Fatalf("delivered summary = %+v, want one", sum)
	}
	if len(delivered) != 1 || delivered[0] != overdue {
		t.Fatalf("breach webhook not delivered for the overdue case: %v (want [%s])", delivered, overdue)
	}
	delivered = nil
	if _, err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 0 {
		t.Fatalf("second tick re-delivered: %v", delivered)
	}
}

func TestRetryableBreachDeliverySurvivesRestartWithoutDuplicatingSuccess(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}
	h := command.NewHandler(log)
	caseID, _, err := h.RequestReview(ctx, id, domain.RequestReview{
		CompanyName: "Acme", CaseType: "aml", SLADays: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().AddDate(0, 0, 10)
	rebuild := func() store.Store {
		st := store.NewMemory()
		if err := projection.New(log, st, cases.Projector{}).Start(ctx); err != nil {
			t.Fatal(err)
		}
		return st
	}

	attempts := 0
	first := (&Scheduler{store: rebuild(), cmd: h, now: func() time.Time { return now }}).WithNotify(
		func(_ context.Context, _ identity.Identity, got string) (DeliveryOutcome, error) {
			attempts++
			if got != caseID {
				t.Fatalf("delivered case %q, want %q", got, caseID)
			}
			return DeliveryRetry, nil
		})
	sum, err := first.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Breached != 1 || sum.Retrying != 1 || attempts != 1 {
		t.Fatalf("retry tick = %+v attempts=%d", sum, attempts)
	}

	// New command/scheduler/store instances simulate a process restart and replay.
	second := (&Scheduler{
		store: rebuild(), cmd: command.NewHandler(log), now: func() time.Time { return now },
	}).WithNotify(func(_ context.Context, _ identity.Identity, _ string) (DeliveryOutcome, error) {
		attempts++
		return DeliverySucceeded, nil
	})
	sum, err = second.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Breached != 0 || sum.Delivered != 1 || attempts != 2 {
		t.Fatalf("restart delivery = %+v attempts=%d", sum, attempts)
	}

	projected := rebuild()
	view, ok, err := cases.Read(ctx, projected, id, caseID)
	if err != nil || !ok {
		t.Fatalf("read delivered case: ok=%v err=%v", ok, err)
	}
	if view.SLAEscalationStatus != events.SLAEscalationDelivered {
		t.Fatalf("projected delivery status = %q", view.SLAEscalationStatus)
	}

	if _, err := second.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("successful escalation was delivered again: attempts=%d", attempts)
	}
}
