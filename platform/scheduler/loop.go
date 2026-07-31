// SPDX-License-Identifier: AGPL-3.0-or-later

// Package scheduler owns the common timed-loop shell used by background
// schedulers. Domain-specific Tick functions remain in their component packages.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/leader"
	"github.com/e6qu/intraktible/platform/metrics"
)

// Run calls tick on each interval until cancellation, reports the latest health
// result, records the shared scheduler metric, and delegates successful summary
// logging to the component.
func Run[T any](
	ctx context.Context,
	interval time.Duration,
	metricName, logName string,
	report func(error),
	tick func(context.Context) (T, error),
	onSuccess func(T),
) {
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			result, err := tick(ctx)
			report(err)
			if err != nil {
				metrics.RecordSchedulerTick(metricName, "error")
				slog.Error(logName+" scheduler tick failed", "err", err)
				continue
			}
			metrics.RecordSchedulerTick(metricName, "ok")
			onSuccess(result)
		}
	}
}

// Leader bundles the event-log claim spine a leader-elected sweep loop needs:
// the log to fence claims, the identity the claims are attributed to, and the
// holder name that distinguishes this replica. One Leader is shared by every
// sweep on a process; each sweep elects its own leader per epoch by role name.
type Leader struct {
	Log    eventlog.Log
	ID     identity.Identity
	Holder string
	Now    func() time.Time
}

// RunGated is the leader-elected sweep loop for a tick returning only an error
// (no summary). It is the shared shell the error-only custom-loop sweeps use so
// they don't each duplicate the gate+tick+metric loop.
func RunGated(
	ctx context.Context,
	ldr *Leader,
	role string,
	interval time.Duration,
	metricName string,
	report func(error),
	tick func(context.Context) error,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			won, err := ldr.Gate(ctx, role, interval, report)
			if err != nil {
				metrics.RecordSchedulerTick(metricName, "error")
				continue
			}
			if !won {
				continue
			}
			if err := tick(ctx); err != nil {
				metrics.RecordSchedulerTick(metricName, "error")
				report(err)
			} else {
				metrics.RecordSchedulerTick(metricName, "ok")
				report(nil)
			}
		}
	}
}

// Gate attempts to win the current epoch's leader claim for role and returns
// whether this replica should execute its tick. A nil receiver is always
// permitted (the single-node configuration). It is the per-tick leader check
// the custom-loop sweeps call before their tick.
func (l *Leader) Gate(
	ctx context.Context,
	role string,
	interval time.Duration,
	report func(error),
) (bool, error) {
	if l == nil {
		return true, nil
	}
	won, err := leader.TryClaim(ctx, l.Log, l.ID, role, l.Holder, interval, l.Now)
	if err != nil {
		report(err)
		return false, err
	}
	if !won {
		report(nil) // standing by is healthy
	}
	return won, nil
}

// RunLeader calls tick on each interval until cancellation, electing one leader
// per epoch across redundant replicas when a Leader is set (the production HA
// case). With a nil Leader it runs unconditionally — the single-node configuration,
// where the one process is trivially the leader. This keeps single-process and
// test deployments running without a separate code path.
func RunLeader[T any](
	ctx context.Context,
	ldr *Leader,
	interval time.Duration,
	metricName, logName string,
	report func(error),
	tick func(context.Context) (T, error),
	onSuccess func(T),
) {
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if ldr != nil {
				won, err := leader.TryClaim(ctx, ldr.Log, ldr.ID, logName, ldr.Holder, interval, ldr.Now)
				if err != nil {
					metrics.RecordSchedulerTick(metricName, "error")
					slog.Error(logName+" leader claim failed", "err", err)
					report(err)
					continue
				}
				if !won {
					// Another replica leads this epoch; standing by is healthy.
					report(nil)
					continue
				}
			}
			result, err := tick(ctx)
			report(err)
			if err != nil {
				metrics.RecordSchedulerTick(metricName, "error")
				slog.Error(logName+" scheduler tick failed", "err", err)
				continue
			}
			metrics.RecordSchedulerTick(metricName, "ok")
			onSuccess(result)
		}
	}
}

// RunWorker polls tick until cancellation; a productive tick re-polls
// immediately while an idle tick waits one interval. Tick errors other than
// cancellation are logged and metered without stopping the loop.
func RunWorker(
	ctx context.Context,
	interval time.Duration,
	metricName, logName, worker string,
	tick func(context.Context, string) (bool, error),
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		worked, err := tick(ctx, worker)
		if err != nil && !errors.Is(err, context.Canceled) {
			metrics.RecordSchedulerTick(metricName, "error")
			slog.Error(logName+" worker tick failed", "worker", worker, "error", err)
		}
		if worked {
			metrics.RecordSchedulerTick(metricName, "ok")
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
