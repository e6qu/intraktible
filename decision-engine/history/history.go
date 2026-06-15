// SPDX-License-Identifier: AGPL-3.0-or-later

// Package history is the Decision Engine's decision-history read model: a
// projector that folds the decision event stream into per-decision records
// (request, per-node trace, and final response) for querying and node-level
// replay, mirroring the documented DecisionRecord shape.
package history

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection is the store collection holding decision records.
const Collection = "decision_history"

// NodeRecord is one node's evaluation within a decision.
type NodeRecord struct {
	NodeID string          `json:"node_id"`
	Type   events.NodeType `json:"type"`
	Output json.RawMessage `json:"output,omitempty"`
}

// Record is the materialized history of one decision.
type Record struct {
	Org         string          `json:"org"`
	Workspace   string          `json:"workspace"`
	DecisionID  string          `json:"decision_id"`
	FlowID      string          `json:"flow_id"`
	Slug        string          `json:"slug"`
	Version     int             `json:"version"`
	Environment string          `json:"environment"`
	Variant     string          `json:"variant,omitempty"` // champion | challenger
	Status      string          `json:"status"`            // started | completed | failed
	Data        json.RawMessage `json:"data,omitempty"`
	Output      json.RawMessage `json:"output,omitempty"`
	Error       string          `json:"error,omitempty"`
	TimeOrdered []string        `json:"time_ordered"`
	Nodes       []NodeRecord    `json:"nodes"`
	StartedAt   time.Time       `json:"started_at"`
	EndedAt     time.Time       `json:"ended_at,omitempty"`
	DurationMS  int64           `json:"duration_ms,omitempty"`
}

// Projector folds decision events into Record documents.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "decision_history" }

// Collections lists the store collection this projector owns.
func (Projector) Collections() []string { return []string{Collection} }

// Apply maintains the decision record across its lifecycle events.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeDecisionStarted:
		return applyStarted(ctx, e, s)
	case events.TypeNodeEvaluated:
		return applyNode(ctx, e, s)
	case events.TypeDecisionCompleted:
		return applyCompleted(ctx, e, s)
	case events.TypeDecisionFailed:
		return applyFailed(ctx, e, s)
	default:
		return nil
	}
}

// decode unmarshals an event payload into T, wrapping decode errors with the seq.
func decode[T any](e eventlog.Envelope) (T, error) {
	var p T
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return p, fmt.Errorf("decision_history: decode %s seq %d: %w", e.Type, e.Seq, err)
	}
	return p, nil
}

func applyStarted(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.DecisionStarted](e)
	if err != nil {
		return err
	}
	r := Record{
		Org: e.Org, Workspace: e.Workspace,
		DecisionID: p.DecisionID, FlowID: p.FlowID, Slug: p.Slug,
		Version: p.Version, Environment: p.Environment, Variant: p.Variant, Status: "started",
		Data: p.Data, TimeOrdered: []string{}, Nodes: []NodeRecord{}, StartedAt: e.Time,
	}
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, r.DecisionID), r)
}

func applyNode(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.NodeEvaluated](e)
	if err != nil {
		return err
	}
	return update(ctx, s, e, p.DecisionID, func(r *Record) {
		r.TimeOrdered = append(r.TimeOrdered, p.NodeID)
		r.Nodes = append(r.Nodes, NodeRecord{NodeID: p.NodeID, Type: p.NodeType, Output: p.Output})
	})
}

func applyCompleted(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.DecisionCompleted](e)
	if err != nil {
		return err
	}
	return update(ctx, s, e, p.DecisionID, func(r *Record) {
		r.Status, r.Output, r.EndedAt, r.DurationMS = "completed", p.Output, e.Time, p.DurationMS
	})
}

func applyFailed(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	p, err := decode[events.DecisionFailed](e)
	if err != nil {
		return err
	}
	return update(ctx, s, e, p.DecisionID, func(r *Record) {
		r.Status, r.Error, r.EndedAt, r.DurationMS = "failed", p.Error, e.Time, p.DurationMS
	})
}

// Read returns one decision record for id's tenant.
func Read(ctx context.Context, s store.Store, id identity.Identity, decisionID string) (Record, bool, error) {
	return store.GetDoc[Record](ctx, s, Collection, store.Key(id.Org, id.Workspace, decisionID))
}

// List returns decisions for id's tenant, most recent first.
func List(ctx context.Context, s store.Store, id identity.Identity) ([]Record, error) {
	out, err := store.ListDocs[Record](ctx, s, Collection, store.Key(id.Org, id.Workspace, ""))
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

func update(ctx context.Context, s store.Store, e eventlog.Envelope, decisionID string, mutate func(*Record)) error {
	key := store.Key(e.Org, e.Workspace, decisionID)
	r, ok, err := store.GetDoc[Record](ctx, s, Collection, key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("decision_history: event seq %d for unknown decision %q", e.Seq, decisionID)
	}
	mutate(&r)
	return store.PutDoc(ctx, s, Collection, key, r)
}
