// SPDX-License-Identifier: AGPL-3.0-or-later

package experiments

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	platformscheduler "github.com/e6qu/intraktible/platform/scheduler"
	"github.com/e6qu/intraktible/platform/store"
)

// Scheduler closes running cohorts at their configured stop window. Start
// windows need no mutation: Resolve begins assignment exactly at StartAt.
type Scheduler struct {
	handler *Handler
	store   store.Store
	now     func() time.Time
	leader  *platformscheduler.Leader
}

func NewScheduler(handler *Handler, st store.Store) *Scheduler {
	return &Scheduler{
		handler: handler, store: st,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// WithLeader elects one leader per sweep epoch across redundant replicas.
func (s *Scheduler) WithLeader(ldr *platformscheduler.Leader) *Scheduler {
	s.leader = ldr
	return s
}

func (s *Scheduler) WithNow(now func() time.Time) *Scheduler {
	s.now = now
	return s
}

func (s *Scheduler) Tick(ctx context.Context) error {
	records, err := s.store.List(ctx, Collection, "")
	if err != nil {
		return err
	}
	for _, record := range records {
		var view View
		if err := json.Unmarshal(record.Doc, &view); err != nil {
			return err
		}
		if view.State != StateRunning || view.Spec.StopAt == nil || s.now().Before(*view.Spec.StopAt) {
			continue
		}
		id, err := identity.New(view.Org, view.Workspace, "experiment-scheduler")
		if err != nil {
			return err
		}
		_, err = s.handler.Complete(ctx, id, view.ExperimentID, "configured stop window elapsed")
		if err != nil && !errors.Is(err, eventlog.ErrConflict) {
			// The projection may still show running immediately after another
			// scheduler tick appended completion. Reconcile against the event-sourced
			// aggregate before declaring the scheduler unhealthy.
			current, ok, foldErr := s.handler.foldOne(ctx, id, view.ExperimentID)
			if foldErr != nil {
				return foldErr
			}
			if !ok || current.state != StateCompleted {
				return err
			}
		}
	}
	return nil
}

func (s *Scheduler) Run(ctx context.Context, interval time.Duration, report func(error)) {
	platformscheduler.RunGated(ctx, s.leader, "experiment_windows", interval, "experiment_windows", report, s.Tick)
}
