// SPDX-License-Identifier: AGPL-3.0-or-later

package schedule

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/events"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/metrics"
	"github.com/e6qu/intraktible/platform/store"
)

// sweepActor is the synthetic actor attributed to scheduler-driven SLA breaches in
// the audit trail (it bypasses HTTP role checks, like the other schedulers).
const sweepActor = "sla-sweeper"

// Cmd is the narrow command surface the scheduler drives (kept narrow to avoid a
// command↔schedule import cycle and to make the scheduler testable with a fake).
type Cmd interface {
	SweepSLA(ctx context.Context, id identity.Identity, now time.Time) ([]string, error)
	PendingSLAEscalations(ctx context.Context, id identity.Identity) ([]string, error)
	RecordSLAEscalation(ctx context.Context, id identity.Identity, caseID string, status events.SLAEscalationStatus) error
}

// Scheduler records SLA breaches for open cases whose deadline has passed, across
// every tenant, on a timer.
type Scheduler struct {
	store  store.Store
	cmd    Cmd
	now    func() time.Time
	notify func(ctx context.Context, id identity.Identity, caseID string) (DeliveryOutcome, error)
}

// NewScheduler builds an SLA-sweep scheduler over the store and command surface.
func NewScheduler(st store.Store, cmd Cmd) *Scheduler {
	return &Scheduler{store: st, cmd: cmd, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the clock (deterministic tests, the demo seeder) and
// returns the scheduler.
func (s *Scheduler) WithNow(now func() time.Time) *Scheduler {
	s.now = now
	return s
}

// WithNotify registers a delivery hook called (in the shell) with the cases that just
// breached their SLA, so an overdue human task can be pushed to an external channel
// (a webhook) — the reviewer-facing escalation. The in-app inbox is driven separately
// off the same events by the notifications projector.
func (s *Scheduler) WithNotify(fn func(ctx context.Context, id identity.Identity, caseID string) (DeliveryOutcome, error)) *Scheduler {
	s.notify = fn
	return s
}

// DeliveryOutcome tells the scheduler whether an external effect should be
// retried or durably completed for the case.
type DeliveryOutcome string

const (
	DeliveryRetry            DeliveryOutcome = "retry"
	DeliverySucceeded        DeliveryOutcome = "delivered"
	DeliveryNoChannel        DeliveryOutcome = "no_channel"
	DeliveryPermanentFailure DeliveryOutcome = "permanent_failure"
)

// TickSummary reports what one sweep did.
type TickSummary struct {
	Breached          int
	Delivered         int
	NoChannel         int
	PermanentFailures int
	Retrying          int
}

// Tick sweeps every tenant's open cases once, recording SLA breaches. SweepSLA is
// idempotent per case, so repeated ticks do not double-emit. Exported for
// deterministic tests.
func (s *Scheduler) Tick(ctx context.Context) (TickSummary, error) {
	views, err := cases.ListAll(ctx, s.store)
	if err != nil {
		return TickSummary{}, err
	}
	tenants := make(map[identity.Identity]struct{})
	for _, v := range views {
		tenants[identity.Identity{Org: v.Org, Workspace: v.Workspace, Actor: sweepActor}] = struct{}{}
	}
	now := s.now()
	var sum TickSummary
	for id := range tenants {
		breached, err := s.cmd.SweepSLA(ctx, id, now)
		if err != nil {
			return sum, err
		}
		sum.Breached += len(breached)
		pending, err := s.cmd.PendingSLAEscalations(ctx, id)
		if err != nil {
			return sum, err
		}
		for _, caseID := range pending {
			outcome := DeliveryNoChannel
			if s.notify != nil {
				outcome, err = s.notify(ctx, id, caseID)
				if err != nil {
					return sum, err
				}
			}
			if outcome == DeliveryRetry {
				sum.Retrying++
				continue
			}
			var status events.SLAEscalationStatus
			switch outcome {
			case DeliverySucceeded:
				status = events.SLAEscalationDelivered
				sum.Delivered++
			case DeliveryNoChannel:
				status = events.SLAEscalationNoChannel
				sum.NoChannel++
			case DeliveryPermanentFailure:
				status = events.SLAEscalationPermanentFailure
				sum.PermanentFailures++
			default:
				return sum, fmt.Errorf("sla sweeper: unknown delivery outcome %q", outcome)
			}
			if err := s.cmd.RecordSLAEscalation(ctx, id, caseID, status); err != nil {
				return sum, err
			}
		}
	}
	return sum, nil
}

// Run sweeps on a timer until ctx is cancelled. Failed ticks remain visible in
// operational health until a later tick completes successfully.
func (s *Scheduler) Run(ctx context.Context, interval time.Duration, report func(error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			summary, err := s.Tick(ctx)
			if err != nil {
				report(err)
				slog.Error("sla sweeper: tick", "err", err)
				metrics.RecordSchedulerTick("case_sla", "error")
				continue
			}
			report(nil)
			if summary.Breached > 0 || summary.Delivered > 0 || summary.Retrying > 0 ||
				summary.PermanentFailures > 0 {
				slog.Info("sla sweeper", "breached", summary.Breached, "delivered", summary.Delivered,
					"retrying", summary.Retrying, "permanent_failures", summary.PermanentFailures)
			}
			metrics.RecordSchedulerTick("case_sla", "ok")
		}
	}
}
