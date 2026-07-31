// SPDX-License-Identifier: AGPL-3.0-or-later

package erasure

import (
	"context"
	"log/slog"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
	platformscheduler "github.com/e6qu/intraktible/platform/scheduler"
)

// retentionSweeper is the synthetic actor attributed to scheduler-driven retention
// erasures (it bypasses HTTP role checks, like the other schedulers).
const retentionSweeper = "retention-sweeper"

// Scheduler applies each tenant's retention policy on a timer: it crypto-shreds
// subjects older than the policy's window, skipping any under a legal hold. A tenant
// with no policy (or zero days) is never swept — retention is opt-in and off by
// default, so the timer never erases data no one asked to expire.
type Scheduler struct {
	vault  *Vault
	gate   RetentionGate
	now    func() time.Time
	leader *platformscheduler.Leader
}

// NewScheduler builds a retention scheduler over the vault.
func NewScheduler(v *Vault) *Scheduler {
	return &Scheduler{vault: v, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the clock (deterministic tests) and returns the scheduler.
func (s *Scheduler) WithNow(now func() time.Time) *Scheduler {
	s.now = now
	return s
}

// WithLeader elects one leader per sweep epoch across redundant replicas.
func (s *Scheduler) WithLeader(ldr *platformscheduler.Leader) *Scheduler {
	s.leader = ldr
	return s
}

// WithRetentionGate applies the same statutory hold used by the one-subject and
// manual bulk erasure paths.
func (s *Scheduler) WithRetentionGate(g RetentionGate) *Scheduler {
	s.gate = g
	return s
}

// TickSummary reports what one sweep did.
type TickSummary struct {
	Erased            int
	Held              int
	StatutoryRetained int
}

// Tick applies every configured retention policy once. RetentionSweep is idempotent
// (an already-erased subject is skipped) and legal-hold-aware, so repeated ticks are
// safe. Exported for deterministic tests.
func (s *Scheduler) Tick(ctx context.Context) (TickSummary, error) {
	policies, err := s.vault.ListRetentionPolicies(ctx)
	if err != nil {
		return TickSummary{}, err
	}
	var sum TickSummary
	for _, p := range policies {
		if p.RetentionDays <= 0 {
			continue
		}
		id := identity.Identity{Org: p.Org, Workspace: p.Workspace, Actor: retentionSweeper}
		result, err := s.vault.RetentionSweep(ctx, id, time.Duration(p.RetentionDays)*24*time.Hour, s.gate)
		if err != nil {
			return sum, err
		}
		if result.Erased > 0 {
			slog.Info("erasure: retention sweep erased expired subjects",
				"org", p.Org, "workspace", p.Workspace, "retention_days", p.RetentionDays,
				"erased", result.Erased, "held", result.Held, "statutory_retained", result.StatutoryRetained)
		}
		sum.Erased += result.Erased
		sum.Held += result.Held
		sum.StatutoryRetained += result.StatutoryRetained
	}
	return sum, nil
}

// Run applies retention on a timer until ctx is cancelled. A transient store
// error does not stop retries, but it remains visible in operational health until
// a later tick succeeds.
func (s *Scheduler) Run(ctx context.Context, interval time.Duration, report func(error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			won, gateErr := s.leader.Gate(ctx, "data_retention", interval, report)
			if gateErr != nil {
				report(gateErr)
				continue
			}
			if !won {
				continue
			}
			if _, err := s.Tick(ctx); err != nil {
				report(err)
				slog.Error("erasure: retention sweep failed", "err", err)
			} else {
				report(nil)
			}
		}
	}
}
