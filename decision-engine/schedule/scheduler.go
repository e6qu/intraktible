// SPDX-License-Identifier: AGPL-3.0-or-later

package schedule

import (
	"context"
	"log/slog"
	"time"

	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/metrics"
	"github.com/e6qu/intraktible/platform/store"
)

// scheduleActor is the synthetic actor attributed to scheduler-driven deploys and
// reverts in the audit trail (it bypasses HTTP role checks, like the other
// schedulers).
const scheduleActor = "deploy-scheduler"

// Cmd is the narrow command surface the scheduler drives (kept narrow to avoid a
// command↔schedule import cycle and to make the scheduler testable with a fake).
type Cmd interface {
	ActivateSchedule(ctx context.Context, id identity.Identity, scheduleID, flowID, env string, version, priorVersion int) error
	RevertSchedule(ctx context.Context, id identity.Identity, scheduleID, flowID, env string, priorVersion int) error
}

// Scheduler activates due scheduled deploys and reverts expired time-boxed ones.
type Scheduler struct {
	store store.Store
	cmd   Cmd
	now   func() time.Time
}

// NewScheduler builds a deploy scheduler over the store and command surface.
func NewScheduler(st store.Store, cmd Cmd) *Scheduler {
	return &Scheduler{store: st, cmd: cmd, now: func() time.Time { return time.Now().UTC() }}
}

// TickSummary reports what one sweep did.
type TickSummary struct {
	Activated int
	Reverted  int
}

// Tick sweeps all schedules once: activates pending schedules whose time has
// arrived, and reverts active time-boxed schedules whose window has elapsed.
// Exported for deterministic tests.
func (s *Scheduler) Tick(ctx context.Context) (TickSummary, error) {
	views, err := ListAll(ctx, s.store)
	if err != nil {
		return TickSummary{}, err
	}
	now := s.now()
	var sum TickSummary
	for _, v := range views {
		id := identity.Identity{Org: v.Org, Workspace: v.Workspace, Actor: scheduleActor}
		switch v.Status {
		case StatusPending:
			if v.At.After(now) {
				continue // not due yet
			}
			prior := s.currentLive(ctx, id, v.FlowID, v.Environment)
			if err := s.cmd.ActivateSchedule(ctx, id, v.ScheduleID, v.FlowID, v.Environment, v.Version, prior); err != nil {
				return sum, err
			}
			sum.Activated++
		case StatusActive:
			if v.Until == nil || v.Until.After(now) {
				continue // not time-boxed, or window still open
			}
			if err := s.cmd.RevertSchedule(ctx, id, v.ScheduleID, v.FlowID, v.Environment, v.PriorVersion); err != nil {
				return sum, err
			}
			sum.Reverted++
		}
	}
	return sum, nil
}

// currentLive reads the version currently live in an environment (0 when none),
// captured as the revert target before a time-boxed deploy goes live.
func (s *Scheduler) currentLive(ctx context.Context, id identity.Identity, flowID, env string) int {
	fv, ok, err := flows.Read(ctx, s.store, id, flowID)
	if err != nil || !ok {
		return 0
	}
	return fv.Deployments[env].Version
}

// Run sweeps on a timer until ctx is cancelled (errors are logged, not fatal).
func (s *Scheduler) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			summary, err := s.Tick(ctx)
			if err != nil {
				slog.Error("deploy scheduler: tick", "err", err)
				metrics.RecordSchedulerTick("deploy_schedule", "error")
				continue
			}
			if summary.Activated > 0 || summary.Reverted > 0 {
				slog.Info("deploy scheduler", "activated", summary.Activated, "reverted", summary.Reverted)
			}
			metrics.RecordSchedulerTick("deploy_schedule", "ok")
		}
	}
}
