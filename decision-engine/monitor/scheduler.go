// SPDX-License-Identifier: AGPL-3.0-or-later

package monitor

import (
	"context"
	"log/slog"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/metrics"
	"github.com/e6qu/intraktible/platform/store"
)

// Scheduler periodically evaluates every monitor and notifies on the firing edge.
// Unlike the on-demand check endpoint, it dedups: it delivers only when a monitor
// crosses ok→firing (recording an Alerted event) and resets on firing→ok (a
// Resolved event), so a steadily-firing monitor is not re-sent every tick.
type Scheduler struct {
	store    store.Store
	cmd      *Handler
	notifier notifier
	now      func() time.Time
}

// NewScheduler builds a Scheduler. notifier may be nil (it then only maintains the
// alert/resolve state without delivering).
func NewScheduler(st store.Store, cmd *Handler, n notifier) *Scheduler {
	return &Scheduler{store: st, cmd: cmd, notifier: n, now: func() time.Time { return time.Now().UTC() }}
}

// TickSummary reports what one sweep did (for logging and tests).
type TickSummary struct {
	Flows     int
	Alerted   int
	Resolved  int
	Delivered int
}

// transitionAction is the dedup decision for one monitor.
type transitionAction int

const (
	actionNone transitionAction = iota
	actionAlert
	actionResolve
)

func transition(firing, alerting bool) transitionAction {
	switch {
	case firing && !alerting:
		return actionAlert
	case !firing && alerting:
		return actionResolve
	default:
		return actionNone
	}
}

type tenantFlow struct{ org, ws, flow string }

// Tick performs one sweep across all tenants' monitors. It is exported so it can
// be driven deterministically in tests (Run wraps it on a ticker).
func (s *Scheduler) Tick(ctx context.Context) (TickSummary, error) {
	all, err := ListAll(ctx, s.store)
	if err != nil {
		return TickSummary{}, err
	}
	groups := map[tenantFlow][]View{}
	var order []tenantFlow
	for _, v := range all {
		k := tenantFlow{v.Org, v.Workspace, v.FlowID}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], v)
	}

	var sum TickSummary
	for _, k := range order {
		id := identity.Identity{Org: k.org, Workspace: k.ws, Actor: "scheduler"}
		snap, err := LoadSnapshot(ctx, s.store, id, k.flow)
		if err != nil {
			return sum, err
		}
		var fired []firedMonitor
		var toAlert []View
		for _, v := range groups[k] {
			st := Evaluate(snap, v.Rule())
			switch transition(st.Firing, v.Alerting) {
			case actionAlert:
				fired = append(fired, firedFrom(v, st))
				toAlert = append(toAlert, v)
			case actionResolve:
				if _, err := s.cmd.MarkResolved(ctx, id, v.FlowID, v.MonitorID); err != nil {
					return sum, err
				}
				sum.Resolved++
			case actionNone:
			}
		}
		// Deliver before recording the alert transition: if delivery fails we return
		// without marking alerted, so the next tick re-delivers rather than the
		// monitor being deduped into silence with the operator never notified.
		if len(fired) > 0 && s.notifier != nil {
			payload := map[string]any{"flow_id": k.flow, "checked_at": s.now(), "fired": fired}
			if _, err := s.notifier.Deliver(ctx, id, "monitor scheduler", payload); err != nil {
				return sum, err
			}
			sum.Delivered++
		}
		for _, v := range toAlert {
			if _, err := s.cmd.MarkAlerted(ctx, id, v.FlowID, v.MonitorID); err != nil {
				return sum, err
			}
			sum.Alerted++
		}
		sum.Flows++
	}
	return sum, nil
}

// Run sweeps on every tick of interval until ctx is cancelled. A tick error is
// logged and the loop continues (a transient store/delivery failure must not kill
// the scheduler).
func (s *Scheduler) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	slog.Info("monitor scheduler started", "interval", interval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if sum, err := s.Tick(ctx); err != nil {
				metrics.RecordSchedulerTick("flow_monitor", "error")
				slog.Error("monitor scheduler tick failed", "err", err)
			} else {
				metrics.RecordSchedulerTick("flow_monitor", "ok")
				if sum.Alerted > 0 || sum.Resolved > 0 {
					slog.Info("monitor scheduler tick", "alerted", sum.Alerted, "resolved", sum.Resolved, "delivered", sum.Delivered)
				}
			}
		}
	}
}
