// SPDX-License-Identifier: AGPL-3.0-or-later

// Package features is the Context Layer's feature engine: a projector that folds
// feature definitions out of the event stream, plus the read-side that computes a
// definition's windowed value from an entity's recorded events. The aggregation
// itself is the pure domain.Compute; this package only wires storage to it.
package features

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/context-layer/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection holds feature-definition documents.
const Collection = "context_features"

// FeatureView is the materialized read model for one feature definition.
type FeatureView struct {
	Org         string    `json:"org"`
	Workspace   string    `json:"workspace"`
	Name        string    `json:"name"`
	EntityType  string    `json:"entity_type"`
	EventName   string    `json:"event_name"`
	Aggregation string    `json:"aggregation"`
	Field       string    `json:"field,omitempty"`
	WindowHours int       `json:"window_hours"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Value is a computed feature value for an entity.
type Value struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

// Projector folds feature definitions into FeatureView documents.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "context_features" }

// Apply maintains the feature-definition read model.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if e.Type != events.TypeFeatureDefined {
		return nil
	}
	var p events.FeatureDefined
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("context-layer: decode %s seq %d: %w", e.Type, e.Seq, err)
	}
	v := FeatureView{
		Org: e.Org, Workspace: e.Workspace,
		Name: p.Name, EntityType: p.EntityType, EventName: p.EventName,
		Aggregation: p.Aggregation, Field: p.Field, WindowHours: p.WindowHours,
		UpdatedAt: e.Time,
	}
	return store.PutDoc(ctx, s, Collection, key(e.Org, e.Workspace, p.EntityType, p.Name), v)
}

// List returns the tenant's feature definitions, optionally filtered by entity
// type, ordered by name.
func List(ctx context.Context, s store.Store, id identity.Identity, entityType string) ([]FeatureView, error) {
	return store.QueryDocs(ctx, s, Collection, store.Key(id.Org, id.Workspace, ""),
		func(f FeatureView) bool { return entityType == "" || f.EntityType == entityType },
		func(a, b FeatureView) bool { return a.Name < b.Name })
}

// Compute evaluates every feature defined for the entity's type against that
// entity's recorded events as of now, returning one Value per definition (ordered
// by name). The windowing is relative to now, so this is a read-time computation.
func Compute(ctx context.Context, s store.Store, id identity.Identity, entityType, entityID string, now time.Time) ([]Value, error) {
	defs, err := List(ctx, s, id, entityType)
	if err != nil {
		return nil, err
	}
	evs, err := entities.ListEvents(ctx, s, id, entityType, entityID)
	if err != nil {
		return nil, err
	}
	inputs := make([]domain.FeatureInput, len(evs))
	for i, ev := range evs {
		inputs[i] = domain.FeatureInput{EventName: ev.EventName, Data: ev.Data, OccurredAt: ev.OccurredAt}
	}
	out := make([]Value, 0, len(defs))
	for _, def := range defs {
		val, err := domain.Compute(domain.FeatureSpec{
			EventName:   def.EventName,
			Aggregation: def.Aggregation,
			Field:       def.Field,
			Window:      time.Duration(def.WindowHours) * time.Hour,
		}, inputs, now)
		if err != nil {
			return nil, fmt.Errorf("context-layer: compute feature %q: %w", def.Name, err)
		}
		out = append(out, Value{Name: def.Name, Value: val})
	}
	return out, nil
}

// Provider adapts the feature engine to a flat name->value lookup for one entity,
// suitable as a decision engine feature source (it satisfies that engine's
// FeatureProvider port structurally, without this package importing it). Now
// defaults to the system clock.
type Provider struct {
	Store store.Store
	Now   func() time.Time
}

// Features computes the entity's feature values as a name->value map.
func (p Provider) Features(ctx context.Context, id identity.Identity, entityType, entityID string) (map[string]float64, error) {
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now()
	}
	vals, err := Compute(ctx, p.Store, id, entityType, entityID, now)
	if err != nil {
		return nil, err
	}
	m := make(map[string]float64, len(vals))
	for _, v := range vals {
		m[v.Name] = v.Value
	}
	return m, nil
}

func key(org, workspace, entityType, name string) string {
	return store.Key(org, workspace, entityType+"/"+name)
}
