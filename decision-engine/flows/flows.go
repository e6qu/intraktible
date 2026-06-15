// SPDX-License-Identifier: AGPL-3.0-or-later

// Package flows is the Decision Engine's flow-registry read model: a projector
// that folds flow lifecycle events into per-tenant flow documents (metadata plus
// the full set of published versions) for the builder UI and the decide path.
package flows

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

// DeploymentView is which version is live in an environment, with an optional
// A/B challenger taking ChallengerPct percent of decisions.
type DeploymentView struct {
	Version           int `json:"version"`
	ChallengerVersion int `json:"challenger_version,omitempty"`
	ChallengerPct     int `json:"challenger_pct,omitempty"`
}

// FlowView is the materialized read model for one flow.
type FlowView struct {
	Org         string                    `json:"org"`
	Workspace   string                    `json:"workspace"`
	FlowID      string                    `json:"flow_id"`
	Slug        string                    `json:"slug"`
	Name        string                    `json:"name"`
	Latest      int                       `json:"latest"`
	Versions    []VersionView             `json:"versions"`
	Deployments map[string]DeploymentView `json:"deployments,omitempty"`
	CreatedAt   time.Time                 `json:"created_at"`
	UpdatedAt   time.Time                 `json:"updated_at"`
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
	case events.TypeFlowVersionDeployed:
		return applyDeployed(ctx, e, s)
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
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.FlowID), fv)
}

func applyPublished(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.FlowVersionPublished
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode published seq %d: %w", e.Seq, err)
	}
	key := store.Key(e.Org, e.Workspace, p.FlowID)
	fv, ok, err := store.GetDoc[FlowView](ctx, s, Collection, key)
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
	return store.PutDoc(ctx, s, Collection, key, fv)
}

func applyDeployed(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.FlowVersionDeployed
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode deployed seq %d: %w", e.Seq, err)
	}
	key := store.Key(e.Org, e.Workspace, p.FlowID)
	fv, ok, err := store.GetDoc[FlowView](ctx, s, Collection, key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("decision_flows: deployed seq %d for unknown flow %q", e.Seq, p.FlowID)
	}
	if fv.Deployments == nil {
		fv.Deployments = make(map[string]DeploymentView)
	}
	fv.Deployments[p.Environment] = DeploymentView{
		Version:           p.Version,
		ChallengerVersion: p.ChallengerVersion,
		ChallengerPct:     p.ChallengerPct,
	}
	fv.UpdatedAt = e.Time
	return store.PutDoc(ctx, s, Collection, key, fv)
}

// Read returns the flow with the given id for id's tenant.
func Read(ctx context.Context, s store.Store, id identity.Identity, flowID string) (FlowView, bool, error) {
	return store.GetDoc[FlowView](ctx, s, Collection, store.Key(id.Org, id.Workspace, flowID))
}

// BySlug returns the flow with the given slug for id's tenant. Slugs are unique
// per tenant, so at most one matches; it is the decide path's flow lookup.
func BySlug(ctx context.Context, s store.Store, id identity.Identity, slug string) (FlowView, bool, error) {
	fvs, err := List(ctx, s, id)
	if err != nil {
		return FlowView{}, false, err
	}
	for _, fv := range fvs {
		if fv.Slug == slug {
			return fv, true, nil
		}
	}
	return FlowView{}, false, nil
}

// List returns all flows for id's tenant, ordered by store key.
func List(ctx context.Context, s store.Store, id identity.Identity) ([]FlowView, error) {
	return store.ListDocs[FlowView](ctx, s, Collection, store.Key(id.Org, id.Workspace, ""))
}
