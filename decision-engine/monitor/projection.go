// SPDX-License-Identifier: AGPL-3.0-or-later

package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection is the monitors read-model collection.
const Collection = "decision_monitors"

// View is a stored monitor definition (its live status is computed at read time
// against the analytics projection, not stored here).
type View struct {
	Org         string    `json:"org"`
	Workspace   string    `json:"workspace"`
	MonitorID   string    `json:"monitor_id"`
	FlowID      string    `json:"flow_id"`
	Metric      string    `json:"metric"`
	Op          string    `json:"op"`
	Threshold   float64   `json:"threshold"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	CreatedBy   string    `json:"created_by"`
}

// Rule projects the stored definition onto the pure evaluator's input.
func (v View) Rule() Rule { return Rule{Metric: v.Metric, Op: v.Op, Threshold: v.Threshold} }

// Projector folds the monitor stream into the read model.
type Projector struct{}

func (Projector) Name() string          { return Collection }
func (Projector) Collections() []string { return []string{Collection} }

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
	}
	return nil
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
