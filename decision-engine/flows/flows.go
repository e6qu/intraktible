// SPDX-License-Identifier: AGPL-3.0-or-later

// Package flows is the Decision Engine's flow-registry read model: a projector
// that folds flow lifecycle events into per-tenant flow documents (metadata plus
// the full set of published versions) for the builder UI and the decide path.
package flows

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection is the store collection holding flow documents.
const Collection = "decision_flows"

// VersionView is one published, immutable flow version in the read model.
type VersionView struct {
	Version     int             `json:"version"`
	Etag        string          `json:"etag"`
	Graph       events.Graph    `json:"graph"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	PublishedAt time.Time       `json:"published_at"`
	PublishedBy string          `json:"published_by"`
}

// FlowView is the materialized read model for one flow.
type FlowView struct {
	Org       string        `json:"org"`
	Workspace string        `json:"workspace"`
	FlowID    string        `json:"flow_id"`
	Slug      string        `json:"slug"`
	Name      string        `json:"name"`
	Latest    int           `json:"latest"`
	Versions  []VersionView `json:"versions"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// Projector folds flow lifecycle events into FlowView documents.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "decision_flows" }

// Apply maintains the flow document. Events of other types are not this
// projector's concern and are skipped (correct routing, not error-swallowing).
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeFlowCreated:
		return applyCreated(ctx, e, s)
	case events.TypeFlowVersionPublished:
		return applyPublished(ctx, e, s)
	default:
		return nil
	}
}

func applyCreated(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.FlowCreated
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode created seq %d: %w", e.Seq, err)
	}
	fv := FlowView{
		Org:       e.Org,
		Workspace: e.Workspace,
		FlowID:    p.FlowID,
		Slug:      p.Slug,
		Name:      p.Name,
		CreatedAt: e.Time,
		UpdatedAt: e.Time,
	}
	return put(ctx, s, e.Org, e.Workspace, p.FlowID, fv)
}

func applyPublished(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.FlowVersionPublished
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode published seq %d: %w", e.Seq, err)
	}
	fv, ok, err := load(ctx, s, e.Org, e.Workspace, p.FlowID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("decision_flows: published seq %d for unknown flow %q", e.Seq, p.FlowID)
	}
	fv.Versions = append(fv.Versions, VersionView{
		Version:     p.Version,
		Etag:        p.Etag,
		Graph:       p.Graph,
		InputSchema: p.InputSchema,
		PublishedAt: e.Time,
		PublishedBy: e.Actor,
	})
	fv.Latest = p.Version
	fv.UpdatedAt = e.Time
	return put(ctx, s, e.Org, e.Workspace, p.FlowID, fv)
}

// Read returns the flow with the given id for id's tenant.
func Read(ctx context.Context, s store.Store, id identity.Identity, flowID string) (FlowView, bool, error) {
	return load(ctx, s, id.Org, id.Workspace, flowID)
}

// List returns all flows for id's tenant, ordered by store key.
func List(ctx context.Context, s store.Store, id identity.Identity) ([]FlowView, error) {
	recs, err := s.List(ctx, Collection)
	if err != nil {
		return nil, err
	}
	prefix := store.Key(id.Org, id.Workspace, "")
	out := make([]FlowView, 0, len(recs))
	for _, rec := range recs {
		if !strings.HasPrefix(rec.Key, prefix) {
			continue
		}
		var fv FlowView
		if err := json.Unmarshal(rec.Doc, &fv); err != nil {
			return nil, fmt.Errorf("decision_flows: decode %q: %w", rec.Key, err)
		}
		out = append(out, fv)
	}
	return out, nil
}

func load(ctx context.Context, s store.Store, org, workspace, flowID string) (FlowView, bool, error) {
	doc, ok, err := s.Get(ctx, Collection, store.Key(org, workspace, flowID))
	if err != nil || !ok {
		return FlowView{}, ok, err
	}
	var fv FlowView
	if err := json.Unmarshal(doc, &fv); err != nil {
		return FlowView{}, false, fmt.Errorf("decision_flows: decode %q: %w", flowID, err)
	}
	return fv, true, nil
}

func put(ctx context.Context, s store.Store, org, workspace, flowID string, fv FlowView) error {
	doc, err := json.Marshal(fv)
	if err != nil {
		return fmt.Errorf("decision_flows: marshal %q: %w", flowID, err)
	}
	return s.Put(ctx, Collection, store.Key(org, workspace, flowID), doc)
}
