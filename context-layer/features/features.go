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
	"sort"
	"time"

	"github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/context-layer/events"
	"github.com/e6qu/intraktible/platform/entity"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection holds feature-definition documents.
const Collection = "context_features"

// FeatureView is the materialized read model for one feature definition. Version is a
// monotonic counter bumped on every (re)definition — its lineage handle: a computed
// value records the version it was produced by, and the materialized cache treats a
// version bump as an invalidation.
type FeatureView struct {
	Org         string    `json:"org"`
	Workspace   string    `json:"workspace"`
	Name        string    `json:"name"`
	EntityType  string    `json:"entity_type"`
	EventName   string    `json:"event_name"`
	Aggregation string    `json:"aggregation"`
	Field       string    `json:"field,omitempty"`
	WindowHours int       `json:"window_hours"`
	Version     int       `json:"version"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Value is a computed feature value for an entity, with its lineage: the definition
// Version it was produced by, how many events fed it (EventCount), and whether it was
// served from the materialized cache rather than a fresh fold.
type Value struct {
	Name       string  `json:"name"`
	Value      float64 `json:"value"`
	Version    int     `json:"version"`
	EventCount int     `json:"event_count"`
	Cached     bool    `json:"cached"`
}

// Projector folds feature definitions into FeatureView documents.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "context_features" }

// Collections lists the store collection this projector owns.
func (Projector) Collections() []string { return []string{Collection} }

// Apply maintains the feature-definition read model.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if e.Type != events.TypeFeatureDefined {
		return nil
	}
	var p events.FeatureDefined
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("context-layer: decode %s seq %d: %w", e.Type, e.Seq, err)
	}
	k := key(e.Org, e.Workspace, p.EntityType, p.Name)
	// Version bumps monotonically on each (re)definition. Derived from prior state (not
	// carried on the event) so it stays deterministic under replay — the fold order is
	// the log order.
	prev, _, err := store.GetDoc[FeatureView](ctx, s, Collection, k)
	if err != nil {
		return err
	}
	v := FeatureView{
		Org: e.Org, Workspace: e.Workspace,
		Name: p.Name, EntityType: p.EntityType, EventName: p.EventName,
		Aggregation: p.Aggregation, Field: p.Field, WindowHours: p.WindowHours,
		Version:   prev.Version + 1,
		UpdatedAt: e.Time,
	}
	return store.PutDoc(ctx, s, Collection, k, v)
}

// List returns the tenant's feature definitions, optionally filtered by entity
// type, ordered by name.
func List(ctx context.Context, s store.Store, id identity.Identity, entityType string) ([]FeatureView, error) {
	return store.QueryDocs(ctx, s, Collection, store.Key(id.Org, id.Workspace, ""),
		func(f FeatureView) bool { return entityType == "" || f.EntityType == entityType },
		func(a, b FeatureView) bool { return a.Name < b.Name })
}

// MaterializedCollection is the feature store's precompute layer: a per-entity
// read-through cache of computed feature values (see ComputeCached). It is derived and
// self-healing — no projector owns it, so a rebuild leaves it to refill on read.
const MaterializedCollection = "context_feature_values"

// farFuture is the expiry of a value that no sliding window can change on its own (a
// feature with no in-window events stays put until a new event bumps the entity count).
var farFuture = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// materialized is one cached feature value plus what keeps it valid: the definition
// Version it was computed against, the entity's event count at cache time (any new
// event bumps it → miss), the instant it was computed (AsOf), and ValidUntil — when a
// sliding window would next change the value absent new events.
type materialized struct {
	Value            float64   `json:"value"`
	Version          int       `json:"version"`
	EntityEventCount int       `json:"entity_event_count"`
	EventCount       int       `json:"event_count"`
	AsOf             time.Time `json:"as_of"`
	ValidUntil       time.Time `json:"valid_until"`
}

// entityCache holds an entity's cached feature values keyed by feature name.
type entityCache struct {
	Values map[string]materialized `json:"values"`
}

func specOf(def FeatureView) domain.FeatureSpec {
	return domain.FeatureSpec{
		EventName:   def.EventName,
		Aggregation: domain.Aggregation(def.Aggregation),
		Field:       def.Field,
		Window:      time.Duration(def.WindowHours) * time.Hour,
	}
}

func entityInputs(ctx context.Context, s store.Store, id identity.Identity, entityType, entityID string) ([]domain.FeatureInput, error) {
	evs, err := entities.ListEvents(ctx, s, id, entityType, entityID)
	if err != nil {
		return nil, err
	}
	inputs := make([]domain.FeatureInput, len(evs))
	for i, ev := range evs {
		inputs[i] = domain.FeatureInput{EventName: ev.EventName, Data: ev.Data, OccurredAt: ev.OccurredAt}
	}
	return inputs, nil
}

// Compute evaluates every feature defined for the entity's type against that entity's
// recorded events AS OF asOf, returning one Value per definition (ordered by name).
// The window's upper bound is asOf, so a past instant yields the POINT-IN-TIME value —
// reproducing what a decision saw. This is always a fresh fold (no cache); use
// ComputeCached for live reads.
func Compute(ctx context.Context, s store.Store, id identity.Identity, entityType, entityID string, asOf time.Time) ([]Value, error) {
	defs, err := List(ctx, s, id, entityType)
	if err != nil {
		return nil, err
	}
	inputs, err := entityInputs(ctx, s, id, entityType, entityID)
	if err != nil {
		return nil, err
	}
	out := make([]Value, 0, len(defs))
	for _, def := range defs {
		res, err := domain.ComputeDetailed(specOf(def), inputs, asOf)
		if err != nil {
			return nil, fmt.Errorf("context-layer: compute feature %q: %w", def.Name, err)
		}
		out = append(out, Value{Name: def.Name, Value: res.Value, Version: def.Version, EventCount: res.EventCount})
	}
	return out, nil
}

// ComputeCached is Compute for a LIVE read (asOf = now) backed by the read-through
// cache: a feature whose cached value is still valid — same definition version, the
// entity has no new events, and now is before the value's window-expiry — is served
// without folding the event stream; the rest are (re)computed and the cache refreshed.
// It is exactly equal to Compute(...now); it only avoids the fold.
func ComputeCached(ctx context.Context, s store.Store, id identity.Identity, entityType, entityID string, now time.Time) ([]Value, error) {
	defs, err := List(ctx, s, id, entityType)
	if err != nil {
		return nil, err
	}
	if len(defs) == 0 {
		return []Value{}, nil
	}
	ent, _, err := entities.ReadEntity(ctx, s, id, entityType, entityID)
	if err != nil {
		return nil, err
	}
	cacheKey := store.Key(id.Org, id.Workspace, entityType+"/"+entityID)
	cache, _, err := store.GetDoc[entityCache](ctx, s, MaterializedCollection, cacheKey)
	if err != nil {
		return nil, err
	}
	if cache.Values == nil {
		cache.Values = map[string]materialized{}
	}
	out := make([]Value, 0, len(defs))
	var miss []FeatureView
	for _, def := range defs {
		m, ok := cache.Values[def.Name]
		if ok && m.Version == def.Version && m.EntityEventCount == ent.EventCount &&
			!now.Before(m.AsOf) && now.Before(m.ValidUntil) {
			out = append(out, Value{Name: def.Name, Value: m.Value, Version: def.Version, EventCount: m.EventCount, Cached: true})
			continue
		}
		miss = append(miss, def)
	}
	if len(miss) > 0 {
		inputs, err := entityInputs(ctx, s, id, entityType, entityID)
		if err != nil {
			return nil, err
		}
		for _, def := range miss {
			res, err := domain.ComputeDetailed(specOf(def), inputs, now)
			if err != nil {
				return nil, fmt.Errorf("context-layer: compute feature %q: %w", def.Name, err)
			}
			validUntil := farFuture
			if res.HasInWindow {
				validUntil = res.OldestInWin.Add(time.Duration(def.WindowHours) * time.Hour)
			}
			cache.Values[def.Name] = materialized{
				Value: res.Value, Version: def.Version, EntityEventCount: ent.EventCount,
				EventCount: res.EventCount, AsOf: now, ValidUntil: validUntil,
			}
			out = append(out, Value{Name: def.Name, Value: res.Value, Version: def.Version, EventCount: res.EventCount})
		}
		if err := store.PutDoc(ctx, s, MaterializedCollection, cacheKey, cache); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
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
func (p Provider) Features(ctx context.Context, id identity.Identity, ref entity.Ref) (map[string]float64, error) {
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now()
	}
	// A decision is a live read → use the read-through cache so repeated decisions on
	// the same entity within a window don't re-fold its whole event stream.
	vals, err := ComputeCached(ctx, p.Store, id, string(ref.Type), string(ref.ID), now)
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
