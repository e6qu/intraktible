// SPDX-License-Identifier: AGPL-3.0-or-later

package experiments

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/e6qu/intraktible/platform/entity"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

const (
	// Collection contains current experiment lifecycle views.
	Collection = "decision_experiments"
	// ExposureCollection contains one reached-treatment fact per decision.
	ExposureCollection = "decision_experiment_exposures"
)

// LaunchRequest is the pending or decided production review.
type LaunchRequest struct {
	RequestID   string    `json:"request_id"`
	Cohort      int       `json:"cohort"`
	Status      string    `json:"status"`
	RequestedBy string    `json:"requested_by"`
	RequestedAt time.Time `json:"requested_at"`
	DecidedBy   string    `json:"decided_by,omitempty"`
	DecidedAt   time.Time `json:"decided_at,omitempty"`
	Reason      string    `json:"reason,omitempty"`
}

// View is the current projection of an experiment.
type View struct {
	Org          string         `json:"org"`
	Workspace    string         `json:"workspace"`
	ExperimentID string         `json:"experiment_id"`
	Cohort       int            `json:"cohort"`
	State        State          `json:"state"`
	Spec         Spec           `json:"spec"`
	CreatedBy    string         `json:"created_by"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedBy    string         `json:"updated_by"`
	UpdatedAt    time.Time      `json:"updated_at"`
	StartedAt    time.Time      `json:"started_at,omitempty"`
	EndedAt      time.Time      `json:"ended_at,omitempty"`
	Launch       *LaunchRequest `json:"launch,omitempty"`
}

// Exposure is the read-side reached-treatment fact.
type Exposure struct {
	Org          string    `json:"org"`
	Workspace    string    `json:"workspace"`
	ExperimentID string    `json:"experiment_id"`
	Cohort       int       `json:"cohort"`
	DecisionID   string    `json:"decision_id"`
	FlowID       string    `json:"flow_id"`
	Environment  string    `json:"environment"`
	ArmKey       string    `json:"arm_key"`
	ArmName      string    `json:"arm_name"`
	ArmKind      ArmKind   `json:"arm_kind"`
	Version      int       `json:"version"`
	SubjectHash  string    `json:"subject_hash"`
	ReachedAt    time.Time `json:"reached_at"`
}

// Projector owns lifecycle and exposure projections.
type Projector struct{}

func (Projector) Name() string          { return "decision_experiments" }
func (Projector) Collections() []string { return []string{Collection, ExposureCollection} }

// Apply folds experiment events and fails loudly on impossible histories.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case TypeCreated:
		p, err := decodeEvent[Created](e)
		if err != nil {
			return err
		}
		return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.ExperimentID), View{
			Org: e.Org, Workspace: e.Workspace,
			ExperimentID: p.ExperimentID, Cohort: 1, State: StateDraft, Spec: p.Spec,
			CreatedBy: e.Actor, CreatedAt: e.Time, UpdatedBy: e.Actor, UpdatedAt: e.Time,
		})
	case TypeUpdated:
		p, err := decodeEvent[Updated](e)
		if err != nil {
			return err
		}
		return mutate(ctx, s, e, p.ExperimentID, func(v *View) error {
			if v.State != StateDraft || p.Cohort != v.Cohort+1 {
				return fmt.Errorf("experiments: invalid update from %s cohort %d to %d", v.State, v.Cohort, p.Cohort)
			}
			v.Cohort, v.Spec, v.Launch = p.Cohort, p.Spec, nil
			return nil
		})
	case TypeLaunchRequested:
		p, err := decodeEvent[LaunchRequested](e)
		if err != nil {
			return err
		}
		return mutate(ctx, s, e, p.ExperimentID, func(v *View) error {
			if v.State != StateDraft || p.Cohort != v.Cohort {
				return fmt.Errorf("experiments: invalid launch request for %s cohort %d", v.State, p.Cohort)
			}
			v.State = StatePendingLaunch
			v.Launch = &LaunchRequest{
				RequestID: p.RequestID, Cohort: p.Cohort, Status: "pending",
				RequestedBy: e.Actor, RequestedAt: e.Time,
			}
			return nil
		})
	case TypeLaunchApproved:
		p, err := decodeEvent[LaunchApproved](e)
		if err != nil {
			return err
		}
		return mutate(ctx, s, e, p.ExperimentID, func(v *View) error {
			if err := validatePending(v, p.RequestID, p.Cohort); err != nil {
				return err
			}
			v.State, v.StartedAt = StateRunning, e.Time
			v.Launch.Status, v.Launch.DecidedBy, v.Launch.DecidedAt, v.Launch.Reason = "approved", e.Actor, e.Time, p.Reason
			return nil
		})
	case TypeLaunchRejected:
		p, err := decodeEvent[LaunchRejected](e)
		if err != nil {
			return err
		}
		return mutate(ctx, s, e, p.ExperimentID, func(v *View) error {
			if err := validatePending(v, p.RequestID, p.Cohort); err != nil {
				return err
			}
			v.State = StateDraft
			v.Launch.Status, v.Launch.DecidedBy, v.Launch.DecidedAt, v.Launch.Reason = "rejected", e.Actor, e.Time, p.Reason
			return nil
		})
	case TypeStarted, TypePaused, TypeResumed, TypeCompleted, TypeCancelled:
		p, err := decodeEvent[Transition](e)
		if err != nil {
			return err
		}
		return applyTransition(ctx, s, e, p)
	case TypeExposureRecorded:
		p, err := decodeEvent[ExposureRecorded](e)
		if err != nil {
			return err
		}
		return store.PutDoc(ctx, s, ExposureCollection, store.Key(e.Org, e.Workspace, p.DecisionID), Exposure{
			Org: e.Org, Workspace: e.Workspace, ExperimentID: p.ExperimentID, Cohort: p.Cohort,
			DecisionID: p.DecisionID, FlowID: p.FlowID, Environment: p.Environment,
			ArmKey: p.ArmKey, ArmName: p.ArmName, ArmKind: p.ArmKind, Version: p.Version,
			SubjectHash: p.SubjectHash, ReachedAt: p.ReachedAt,
		})
	default:
		return nil
	}
}

func applyTransition(ctx context.Context, s store.Store, e eventlog.Envelope, p Transition) error {
	return mutate(ctx, s, e, p.ExperimentID, func(v *View) error {
		if p.Cohort != v.Cohort {
			return fmt.Errorf("experiments: stale cohort %d, current %d", p.Cohort, v.Cohort)
		}
		switch e.Type {
		case TypeStarted:
			if v.State != StateDraft {
				return fmt.Errorf("experiments: cannot start from %s", v.State)
			}
			v.State, v.StartedAt = StateRunning, e.Time
		case TypePaused:
			if v.State != StateRunning {
				return fmt.Errorf("experiments: cannot pause from %s", v.State)
			}
			v.State = StatePaused
		case TypeResumed:
			if v.State != StatePaused {
				return fmt.Errorf("experiments: cannot resume from %s", v.State)
			}
			v.State = StateRunning
		case TypeCompleted:
			if v.State != StateRunning && v.State != StatePaused {
				return fmt.Errorf("experiments: cannot complete from %s", v.State)
			}
			v.State, v.EndedAt = StateCompleted, e.Time
		case TypeCancelled:
			if v.State == StateCompleted || v.State == StateCancelled {
				return fmt.Errorf("experiments: cannot cancel from %s", v.State)
			}
			v.State, v.EndedAt = StateCancelled, e.Time
		}
		return nil
	})
}

func validatePending(v *View, requestID string, cohort int) error {
	if v.State != StatePendingLaunch || v.Launch == nil || v.Launch.Status != "pending" ||
		v.Launch.RequestID != requestID || cohort != v.Cohort {
		return fmt.Errorf("experiments: launch request %q is not pending for cohort %d", requestID, cohort)
	}
	return nil
}

func mutate(ctx context.Context, s store.Store, e eventlog.Envelope, experimentID string, fn func(*View) error) error {
	key := store.Key(e.Org, e.Workspace, experimentID)
	v, ok, err := store.GetDoc[View](ctx, s, Collection, key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("experiments: event %s for unknown experiment %q", e.Type, experimentID)
	}
	if err := fn(&v); err != nil {
		return err
	}
	v.UpdatedBy, v.UpdatedAt = e.Actor, e.Time
	return store.PutDoc(ctx, s, Collection, key, v)
}

func decodeEvent[T any](e eventlog.Envelope) (T, error) {
	var payload T
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return payload, fmt.Errorf("experiments: decode %s seq %d: %w", e.Type, e.Seq, err)
	}
	return payload, nil
}

// Read returns one tenant-scoped experiment.
func Read(ctx context.Context, s store.Store, id identity.Identity, experimentID string) (View, bool, error) {
	return store.GetDoc[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, experimentID))
}

// List returns newest-updated experiments first.
func List(ctx context.Context, s store.Store, id identity.Identity) ([]View, error) {
	items, err := store.ListDocs[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, ""))
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		}
		return items[i].ExperimentID < items[j].ExperimentID
	})
	return items, nil
}

// ListExposures returns exact-cohort reached-treatment facts.
func ListExposures(ctx context.Context, s store.Store, id identity.Identity, experimentID string, cohort int) ([]Exposure, error) {
	return store.QueryDocs(ctx, s, ExposureCollection, store.Key(id.Org, id.Workspace, ""),
		func(exposure Exposure) bool {
			return exposure.ExperimentID == experimentID && exposure.Cohort == cohort
		},
		func(a, b Exposure) bool {
			if !a.ReachedAt.Equal(b.ReachedAt) {
				return a.ReachedAt.Before(b.ReachedAt)
			}
			return a.DecisionID < b.DecisionID
		},
	)
}

// Resolve finds the one running experiment for a flow/environment and assigns
// the subject. Overlapping active experiments fail loudly instead of silently
// splitting the same traffic through two incompatible cohorts.
func Resolve(ctx context.Context, s store.Store, id identity.Identity, flowID, environment string, data map[string]any, ref entity.Ref, now time.Time) (Assignment, bool, error) {
	items, err := List(ctx, s, id)
	if err != nil {
		return Assignment{}, false, err
	}
	var active *View
	for i := range items {
		item := &items[i]
		if item.State != StateRunning || item.Spec.FlowID != flowID || item.Spec.Environment != environment {
			continue
		}
		if item.Spec.StartAt != nil && now.Before(*item.Spec.StartAt) {
			continue
		}
		if item.Spec.StopAt != nil && !now.Before(*item.Spec.StopAt) {
			continue
		}
		if active != nil {
			return Assignment{}, false, fmt.Errorf(
				"experiments: overlapping running experiments %q and %q for flow %q in %s",
				active.ExperimentID, item.ExperimentID, flowID, environment,
			)
		}
		active = item
	}
	if active == nil {
		return Assignment{}, false, nil
	}
	return Assign(active.Spec, active.ExperimentID, active.Cohort, data, ref)
}
