// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"context"
	"log/slog"
	"time"

	"github.com/e6qu/intraktible/decision-engine/notify"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/metrics"
	"github.com/e6qu/intraktible/platform/store"
)

// AlertCmd is the write side the drift scheduler needs (satisfied by the engine
// command.Handler). Kept narrow so the models package does not depend on the
// command package (which would be a cycle — command imports models).
type AlertCmd interface {
	MarkModelDriftAlerted(ctx context.Context, id identity.Identity, name string, psi, threshold float64) (eventlog.Envelope, error)
	MarkModelDriftResolved(ctx context.Context, id identity.Identity, name string) (eventlog.Envelope, error)
}

// Notifier delivers a payload to a tenant's active webhooks. Satisfied by
// notify.Notifier; nil disables delivery (the scheduler then only maintains the
// alert/resolve dedup state).
type Notifier interface {
	Deliver(ctx context.Context, id identity.Identity, reason string, payload any) (notify.DeliverySummary, error)
}

// Scheduler periodically evaluates every model's drift (PSI vs its configured
// threshold) and pushes the ok→firing edge to webhooks. Like the flow monitor
// scheduler it dedups: it delivers only on the crossing (recording a drift-Alerted
// event) and resets on firing→ok (a drift-Resolved event), so a steadily-drifting
// model is not re-sent every tick.
type Scheduler struct {
	store      store.Store
	cmd        AlertCmd
	notifier   Notifier
	windowDays int // drift window for the firing decision (0 = cumulative)
	now        func() time.Time
}

// NewScheduler builds a drift Scheduler. windowDays selects the drift window used
// for the firing decision (0 = all-time cumulative). notifier may be nil.
func NewScheduler(st store.Store, cmd AlertCmd, n Notifier, windowDays int) *Scheduler {
	return &Scheduler{store: st, cmd: cmd, notifier: n, windowDays: windowDays, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the clock (deterministic tests, the demo seeder) and
// returns the scheduler.
func (s *Scheduler) WithNow(now func() time.Time) *Scheduler {
	s.now = now
	return s
}

// TickSummary reports what one sweep did (for logging and tests).
type TickSummary struct {
	Models           int
	Alerted          int
	Resolved         int
	Delivered        int
	DeliveryFailures int // models whose webhook delivery failed this sweep (retried next tick)
}

// firedModel is one model's drift at the moment it crossed into firing.
type firedModel struct {
	Model     string  `json:"model"`
	PSI       float64 `json:"psi"`
	Threshold float64 `json:"threshold"`
	Window    int     `json:"window_days"`
}

// Tick performs one sweep across all tenants' model stats. It is exported so it
// can be driven deterministically in tests (Run wraps it on a ticker).
func (s *Scheduler) Tick(ctx context.Context) (TickSummary, error) {
	all, err := store.ListDocs[ModelStats](ctx, s.store, StatsCollection, "")
	if err != nil {
		return TickSummary{}, err
	}
	var sum TickSummary
	for _, st := range all {
		// A model with no threshold/baseline can never fire; skip it (but it must
		// still resolve if it was alerting and the threshold was later cleared).
		psi, firing, ok := st.Firing(s.windowDays)
		if !ok && !st.Alerting {
			continue
		}
		sum.Models++
		id := identity.Identity{Org: st.Org, Workspace: st.Workspace, Actor: "drift-scheduler"}
		switch {
		case firing && !st.Alerting:
			// Deliver before recording the alert: if delivery fails we skip marking
			// alerted (so the next tick re-delivers) but do NOT abort the sweep — one
			// tenant's down webhook must not starve every other model after it.
			if s.notifier != nil {
				payload := map[string]any{
					"checked_at": s.now(),
					"fired":      []firedModel{{Model: st.Name, PSI: psi, Threshold: st.Threshold, Window: s.windowDays}},
				}
				summary, err := s.notifier.Deliver(ctx, id, "model drift scheduler", payload)
				if err != nil || summary.RetryWorthy() {
					slog.Warn("model drift scheduler: delivery not completed, will retry next tick", "model", st.Name, "err", err)
					sum.DeliveryFailures++
					continue
				}
				// Count Delivered only when an endpoint accepted (an all-permanent sweep
				// records+dedups below but delivered nothing).
				if summary.Delivered() {
					sum.Delivered++
				}
			}
			if _, err := s.cmd.MarkModelDriftAlerted(ctx, id, st.Name, psi, st.Threshold); err != nil {
				return sum, err
			}
			sum.Alerted++
		case !firing && st.Alerting:
			if _, err := s.cmd.MarkModelDriftResolved(ctx, id, st.Name); err != nil {
				return sum, err
			}
			sum.Resolved++
		}
	}
	return sum, nil
}

// Run sweeps on every tick of interval until ctx is cancelled. A tick error is
// logged and the loop continues (a transient store/delivery failure must not kill
// the scheduler).
func (s *Scheduler) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	slog.Info("model drift scheduler started", "interval", interval, "window_days", s.windowDays)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if sum, err := s.Tick(ctx); err != nil {
				metrics.RecordSchedulerTick("model_drift", "error")
				slog.Error("model drift scheduler tick failed", "err", err)
			} else {
				metrics.RecordSchedulerTick("model_drift", "ok")
				if sum.Alerted > 0 || sum.Resolved > 0 {
					slog.Info("model drift scheduler tick", "alerted", sum.Alerted, "resolved", sum.Resolved, "delivered", sum.Delivered)
				}
			}
		}
	}
}
