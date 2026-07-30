// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"log/slog"
	"time"

	"github.com/e6qu/intraktible/platform/metrics"
)

// RecoveryWorker repeatedly claims and repairs interrupted decision generations.
// Its owner identity is stable for the worker lifetime and unique across replicas.
type RecoveryWorker struct {
	handler *DecideHandler
	owner   string
}

// NewRecoveryWorker creates one replica-local recovery worker.
func (h *DecideHandler) NewRecoveryWorker() *RecoveryWorker {
	return &RecoveryWorker{handler: h, owner: h.newID()}
}

// Tick performs one durable recovery scan. It is exposed so the composition root
// can fail startup when the initial scan cannot reach the source of truth.
func (w *RecoveryWorker) Tick(ctx context.Context) (RecoverySummary, error) {
	return w.handler.RecoverInterrupted(ctx, w.owner)
}

// Run scans on interval until cancellation and reports the latest operational
// health. A failed tick does not stop later repair attempts.
func (w *RecoveryWorker) Run(
	ctx context.Context,
	interval time.Duration,
	report func(error),
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			summary, err := w.Tick(ctx)
			report(err)
			if err != nil {
				metrics.RecordSchedulerTick("decision_recovery", "error")
				slog.Error("decision recovery tick failed", "err", err)
				continue
			}
			metrics.RecordSchedulerTick("decision_recovery", "ok")
			if summary.Claimed > 0 {
				slog.Info(
					"decision recovery tick completed",
					"scanned", summary.Scanned,
					"claimed", summary.Claimed,
					"recovered", summary.Recovered,
					"abandoned", summary.Abandoned,
				)
			}
		}
	}
}
