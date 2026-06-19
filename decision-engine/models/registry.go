// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection is the read-model collection for model definitions.
const Collection = "decision_models"

// ModelView is the materialized read model for one model definition.
type ModelView struct {
	Org       string          `json:"org"`
	Workspace string          `json:"workspace"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Spec      json.RawMessage `json:"spec"`
	UpdatedAt string          `json:"updated_at"`
}

// Projector folds ModelDefined events into ModelView documents.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "decision_models" }

// Collections lists the store collections this projector owns (reset on rebuild).
func (Projector) Collections() []string { return []string{Collection} }

// Apply maintains the model registry read model.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if e.Type != events.TypeModelDefined {
		return nil
	}
	var p events.ModelDefined
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("models: decode %s seq %d: %w", e.Type, e.Seq, err)
	}
	var kind string
	if s, err := ParseSpec(p.Spec); err == nil {
		kind = s.Kind
	}
	v := ModelView{
		Org: e.Org, Workspace: e.Workspace,
		Name: p.Name, Kind: kind, Spec: p.Spec, UpdatedAt: e.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.Name), v)
}

// Read returns one model definition for id's tenant.
func Read(ctx context.Context, s store.Store, id identity.Identity, name string) (ModelView, bool, error) {
	return store.GetDoc[ModelView](ctx, s, Collection, store.Key(id.Org, id.Workspace, name))
}

// List returns the tenant's model definitions in name order.
func List(ctx context.Context, s store.Store, id identity.Identity) ([]ModelView, error) {
	return store.QueryDocs(ctx, s, Collection, store.Key(id.Org, id.Workspace, ""),
		nil, func(a, b ModelView) bool { return a.Name < b.Name })
}

// Provider resolves and evaluates a registered model — the adapter the decision
// engine's Predict nodes call (it structurally satisfies the engine's ModelProvider
// port without the engine importing this package's command surface).
type Provider struct {
	Store store.Store
}

// Predict resolves the named model from the registry and evaluates it over the
// features, returning the prediction as JSON (the decision records it for replay).
func (p Provider) Predict(ctx context.Context, id identity.Identity, model string, features map[string]any) (json.RawMessage, error) {
	mv, ok, err := Read(ctx, p.Store, id, model)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("models: unknown model %q", model)
	}
	spec, err := ParseSpec(mv.Spec)
	if err != nil {
		return nil, err
	}
	pred, err := Evaluate(spec, features)
	if err != nil {
		return nil, err
	}
	return json.Marshal(pred)
}
