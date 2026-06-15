// SPDX-License-Identifier: AGPL-3.0-or-later

// Package entities is the Context Layer's read model: a projector that folds the
// context event stream into entity documents (latest merged attributes + an event
// count) and a per-entity log of the custom events recorded about them.
package entities

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/context-layer/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collections held by this read model.
const (
	CollectionEntities = "context_entities"
	CollectionEvents   = "context_events"
)

// EntityView is the materialized read model for one entity: its latest merged
// attributes plus a running count of the events recorded about it.
type EntityView struct {
	Org        string          `json:"org"`
	Workspace  string          `json:"workspace"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	Attributes json.RawMessage `json:"attributes"`
	EventCount int             `json:"event_count"`
	FirstSeen  time.Time       `json:"first_seen"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// EventView is one custom event recorded about an entity.
type EventView struct {
	Org        string          `json:"org"`
	Workspace  string          `json:"workspace"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	EventName  string          `json:"event_name"`
	Data       json.RawMessage `json:"data,omitempty"`
	Seq        uint64          `json:"seq"`
	OccurredAt time.Time       `json:"occurred_at"`
	RecordedAt time.Time       `json:"recorded_at"`
}

// Projector folds context events into entity + event documents.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "context" }

// Apply maintains the entity document and the per-entity event log.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeEntityRecorded:
		return applyEntity(ctx, e, s)
	case events.TypeEventRecorded:
		return applyEvent(ctx, e, s)
	default:
		return nil
	}
}

func applyEntity(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.EntityRecorded
	if err := decode(e, &p); err != nil {
		return err
	}
	return upsertEntity(ctx, s, e, p.EntityType, p.EntityID, func(c *EntityView) error {
		merged, err := domain.MergeAttributes(c.Attributes, p.Attributes)
		if err != nil {
			return err
		}
		c.Attributes = merged
		return nil
	})
}

func applyEvent(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.EventRecorded
	if err := decode(e, &p); err != nil {
		return err
	}
	ev := EventView{
		Org: e.Org, Workspace: e.Workspace,
		EntityType: p.EntityType, EntityID: p.EntityID, EventName: p.EventName,
		Data: p.Data, Seq: e.Seq, OccurredAt: p.OccurredAt, RecordedAt: e.Time,
	}
	if err := store.PutDoc(ctx, s, CollectionEvents, eventKey(e.Org, e.Workspace, p.EntityType, p.EntityID, e.Seq), ev); err != nil {
		return err
	}
	// Recording an event about an entity also touches the entity (auto-creating a
	// shell when the event arrives before any explicit RecordEntity).
	return upsertEntity(ctx, s, e, p.EntityType, p.EntityID, func(c *EntityView) error {
		c.EventCount++
		return nil
	})
}

// upsertEntity loads-or-creates the entity, applies mutate, and persists it.
func upsertEntity(ctx context.Context, s store.Store, e eventlog.Envelope, entityType, entityID string, mutate func(*EntityView) error) error {
	key := store.Key(e.Org, e.Workspace, entKey(entityType, entityID))
	c, ok, err := store.GetDoc[EntityView](ctx, s, CollectionEntities, key)
	if err != nil {
		return err
	}
	if !ok {
		c = EntityView{
			Org: e.Org, Workspace: e.Workspace,
			EntityType: entityType, EntityID: entityID,
			Attributes: json.RawMessage("{}"), FirstSeen: e.Time,
		}
	}
	if err := mutate(&c); err != nil {
		return err
	}
	c.UpdatedAt = e.Time
	return store.PutDoc(ctx, s, CollectionEntities, key, c)
}

// ReadEntity returns one entity for id's tenant.
func ReadEntity(ctx context.Context, s store.Store, id identity.Identity, entityType, entityID string) (EntityView, bool, error) {
	return store.GetDoc[EntityView](ctx, s, CollectionEntities, store.Key(id.Org, id.Workspace, entKey(entityType, entityID)))
}

// ListEntities returns the tenant's entities, optionally filtered by type, most
// recently updated first.
func ListEntities(ctx context.Context, s store.Store, id identity.Identity, entityType string) ([]EntityView, error) {
	all, err := store.ListDocs[EntityView](ctx, s, CollectionEntities, store.Key(id.Org, id.Workspace, ""))
	if err != nil {
		return nil, err
	}
	out := make([]EntityView, 0, len(all))
	for _, c := range all {
		if entityType != "" && c.EntityType != entityType {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// ListEvents returns the events recorded about one entity, newest first.
func ListEvents(ctx context.Context, s store.Store, id identity.Identity, entityType, entityID string) ([]EventView, error) {
	all, err := store.ListDocs[EventView](ctx, s, CollectionEvents, store.Key(id.Org, id.Workspace, entKey(entityType, entityID)+"/"))
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Seq > all[j].Seq })
	return all, nil
}

// entKey is the per-tenant id portion for an entity document.
func entKey(entityType, entityID string) string { return entityType + "/" + entityID }

// eventKey orders an entity's events by Seq within its prefix (zero-padded so
// lexical store ordering matches numeric Seq order).
func eventKey(org, workspace, entityType, entityID string, seq uint64) string {
	return store.Key(org, workspace, fmt.Sprintf("%s/%020d", entKey(entityType, entityID), seq))
}

func decode[T any](e eventlog.Envelope, v *T) error {
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return fmt.Errorf("context-layer: decode %s seq %d: %w", e.Type, e.Seq, err)
	}
	return nil
}
