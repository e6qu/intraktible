// SPDX-License-Identifier: AGPL-3.0-or-later

// Package schedule is the read model + scheduler for scheduled / time-boxed
// deployments: a deploy queued for a future time, optionally auto-reverting after a
// window. The projection folds the schedule lifecycle (scheduled → active →
// reverted | canceled); the Scheduler activates due schedules and reverts expired
// time-boxed ones on a timer.
package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection holds scheduled-deploy documents (keyed by schedule id).
const Collection = "decision_deploy_schedules"

// Status is a schedule's lifecycle state.
type Status string

const (
	StatusPending  Status = "pending"  // queued, not yet activated
	StatusActive   Status = "active"   // activated (deployed); awaits its window if time-boxed
	StatusReverted Status = "reverted" // time-box elapsed, reverted to the prior version
	StatusCanceled Status = "canceled" // canceled before/while active
)

// View is one scheduled deployment.
type View struct {
	Org          string     `json:"org"`
	Workspace    string     `json:"workspace"`
	ScheduleID   string     `json:"schedule_id"`
	FlowID       string     `json:"flow_id"`
	Environment  string     `json:"environment"`
	Version      int        `json:"version"`
	At           time.Time  `json:"at"`
	Until        *time.Time `json:"until,omitempty"`
	Status       Status     `json:"status"`
	PriorVersion int        `json:"prior_version,omitempty"` // live version before activation (for revert)
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	Seq          uint64     `json:"seq"`
}

// Projector folds the scheduled-deploy events into View documents.
type Projector struct{}

func (Projector) Name() string          { return Collection }
func (Projector) Collections() []string { return []string{Collection} }

func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeDeployScheduled:
		var p events.DeployScheduled
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_deploy_schedules: decode scheduled seq %d: %w", e.Seq, err)
		}
		return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.ScheduleID), View{
			Org: e.Org, Workspace: e.Workspace, ScheduleID: p.ScheduleID, FlowID: p.FlowID,
			Environment: p.Environment, Version: p.Version, At: p.At, Until: p.Until,
			Status: StatusPending, CreatedBy: e.Actor, CreatedAt: e.Time, Seq: e.Seq,
		})
	case events.TypeDeployScheduleActivated:
		var p events.DeployScheduleActivated
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_deploy_schedules: decode activated seq %d: %w", e.Seq, err)
		}
		_, err := store.UpdateDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.ScheduleID), func(v *View) {
			v.Status, v.PriorVersion = StatusActive, p.PriorVersion
		})
		return err
	case events.TypeDeployScheduleReverted:
		var p events.DeployScheduleReverted
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_deploy_schedules: decode reverted seq %d: %w", e.Seq, err)
		}
		return setStatus(ctx, s, e, p.ScheduleID, StatusReverted)
	case events.TypeDeployScheduleCanceled:
		var p events.DeployScheduleCanceled
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_deploy_schedules: decode canceled seq %d: %w", e.Seq, err)
		}
		return setStatus(ctx, s, e, p.ScheduleID, StatusCanceled)
	}
	return nil
}

func setStatus(ctx context.Context, s store.Store, e eventlog.Envelope, scheduleID string, status Status) error {
	if scheduleID == "" {
		return fmt.Errorf("decision_deploy_schedules: event seq %d has no schedule id", e.Seq)
	}
	_, err := store.UpdateDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, scheduleID), func(v *View) {
		v.Status = status
	})
	return err
}

// List returns a flow's schedules, newest first.
func List(ctx context.Context, s store.Store, id identity.Identity, flowID string) ([]View, error) {
	return store.QueryDocs(ctx, s, Collection, store.Key(id.Org, id.Workspace, ""),
		func(v View) bool { return flowID == "" || v.FlowID == flowID },
		func(a, b View) bool { return a.Seq > b.Seq })
}

// ListAll returns every tenant's schedules (the scheduler's sweep input).
func ListAll(ctx context.Context, s store.Store) ([]View, error) {
	return store.ListDocs[View](ctx, s, Collection, "")
}
