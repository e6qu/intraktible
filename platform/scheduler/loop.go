// SPDX-License-Identifier: AGPL-3.0-or-later

// Package scheduler owns the common timed-loop shell used by background
// schedulers. Domain-specific Tick functions remain in their component packages.
package scheduler

import (
	"context"
	"log/slog"
	"time"

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
