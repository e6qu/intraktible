// SPDX-License-Identifier: AGPL-3.0-or-later

package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/e6qu/intraktible/decision-engine/analytics"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection is the monitors read-model collection; BaselineCollection holds the
// per-flow drift baselines.
const (
	Collection         = "decision_monitors"
	BaselineCollection = "decision_baselines"
)

// View is a stored monitor definition (its live status is computed at read time
// against the analytics projection, not stored here). Alerting is the last-known
// firing state, maintained by the scheduler's alert/resolve events for dedup.
type View struct {
	Org         string    `json:"org"`
	Workspace   string    `json:"workspace"`
	MonitorID   string    `json:"monitor_id"`
	FlowID      string    `json:"flow_id"`
	Metric      string    `json:"metric"`
	Op          string    `json:"op"`
	Threshold   float64   `json:"threshold"`
	Description string    `json:"description,omitempty"`
	Alerting    bool      `json:"alerting"`
	CreatedAt   time.Time `json:"created_at"`
	CreatedBy   string    `json:"created_by"`
}

// BaselineView is a flow's captured disposition distribution.
type BaselineView struct {
	Org        string    `json:"org"`
	Workspace  string    `json:"workspace"`
	FlowID     string    `json:"flow_id"`
	Approve    float64   `json:"approve"`
	Decline    float64   `json:"decline"`
	Refer      float64   `json:"refer"`
	Total      int       `json:"total"`
	CapturedAt time.Time `json:"captured_at"`
}

// Rule projects the stored definition onto the pure evaluator's input (the
// wire-string metric/op become the named types at this boundary).
func (v View) Rule() Rule {
	return Rule{Metric: Metric(v.Metric), Op: Op(v.Op), Threshold: v.Threshold}
}

// Projector folds the monitor stream into the read model.
type Projector struct{}

func (Projector) Name() string          { return Collection }
func (Projector) Collections() []string { return []string{Collection, BaselineCollection} }

func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case TypeDefined:
		var p Defined
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_monitors: decode defined seq %d: %w", e.Seq, err)
		}
		v := View{
			Org: e.Org, Workspace: e.Workspace, MonitorID: p.MonitorID, FlowID: p.FlowID,
			Metric: p.Metric, Op: p.Op, Threshold: p.Threshold, Description: p.Description,
			CreatedAt: e.Time, CreatedBy: e.Actor,
		}
		return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.MonitorID), v)
	case TypeDeleted:
		var p Deleted
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_monitors: decode deleted seq %d: %w", e.Seq, err)
		}
		return s.Delete(ctx, Collection, store.Key(e.Org, e.Workspace, p.MonitorID))
	case TypeBaselineCaptured:
		var p BaselineCaptured
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_monitors: decode baseline seq %d: %w", e.Seq, err)
		}
		v := BaselineView{
			Org: e.Org, Workspace: e.Workspace, FlowID: p.FlowID,
			Approve: p.Approve, Decline: p.Decline, Refer: p.Refer, Total: p.Total, CapturedAt: e.Time,
		}
		return store.PutDoc(ctx, s, BaselineCollection, store.Key(e.Org, e.Workspace, p.FlowID), v)
	case TypeAlerted:
		return setAlerting(ctx, s, e, TypeAlerted, true)
	case TypeResolved:
		return setAlerting(ctx, s, e, TypeResolved, false)
	}
	return nil
}

// setAlerting flips a monitor's last-known firing state (alert/resolve dedup).
func setAlerting(ctx context.Context, s store.Store, e eventlog.Envelope, typ string, alerting bool) error {
	var p Alerted // Alerted and Resolved share the {monitor_id, flow_id} shape
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_monitors: decode %s seq %d: %w", typ, e.Seq, err)
	}
	_, err := store.UpdateDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.MonitorID), func(v *View) {
		v.Alerting = alerting
	})
	return err // an alert/resolve for a deleted monitor is a no-op
}

// ReadBaseline returns a flow's captured drift baseline, if any.
func ReadBaseline(ctx context.Context, s store.Store, id identity.Identity, flowID string) (BaselineView, bool, error) {
	return store.GetDoc[BaselineView](ctx, s, BaselineCollection, store.Key(id.Org, id.Workspace, flowID))
}

// LoadSnapshot reads the evaluator's input for a flow: live metrics plus the
// drift baseline if one was captured. A zero metrics snapshot (unused flow) is
// fine — rules over it read as "no data".
func LoadSnapshot(ctx context.Context, s store.Store, id identity.Identity, flowID string) (Snapshot, error) {
	m, _, err := analytics.Read(ctx, s, id, flowID)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Metrics: m}
	b, ok, err := ReadBaseline(ctx, s, id, flowID)
	if err != nil {
		return Snapshot{}, err
	}
	if ok {
		snap.Baseline = &Baseline{Approve: b.Approve, Decline: b.Decline, Refer: b.Refer, Total: b.Total}
	}
	return snap, nil
}

// ListAll returns every monitor across all tenants (the scheduler's sweep input;
// each View carries its own Org/Workspace).
func ListAll(ctx context.Context, s store.Store) ([]View, error) {
	return store.ListDocs[View](ctx, s, Collection, "")
}

// ListByFlow returns the monitors defined on a flow, oldest first.
func ListByFlow(ctx context.Context, s store.Store, id identity.Identity, flowID string) ([]View, error) {
	all, err := store.ListDocs[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, ""))
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(all))
	for _, v := range all {
		if v.FlowID == flowID {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
