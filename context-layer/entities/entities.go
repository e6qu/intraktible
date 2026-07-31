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
	CollectionEntities      = "context_entities"
	CollectionEntityHistory = "context_entity_history"
	CollectionEvents        = "context_events"
	CollectionEventIndex    = "context_event_index"
)

// EntityView is the materialized read model for one entity: its latest merged
// attributes plus a running count of the events recorded about it.
type EntityView struct {
	Org           string          `json:"org"`
	Workspace     string          `json:"workspace"`
	EntityType    string          `json:"entity_type"`
	EntityID      string          `json:"entity_id"`
	Attributes    json.RawMessage `json:"attributes"`
	SchemaVersion int             `json:"schema_version,omitempty"`
	SchemaHash    string          `json:"schema_hash,omitempty"`
	EventCount    int             `json:"event_count"`
	FirstSeen     time.Time       `json:"first_seen"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// EntityVersion is one replay-derived entity state after a source fact. Keeping
// the complete merged state makes bitemporal dataset population and segment
// reads independent of the current entity projection.
type EntityVersion struct {
	Entity EntityView `json:"entity"`
	Seq    uint64     `json:"seq"`
}

// EventView is one custom event recorded about an entity.
type EventView struct {
	Org               string          `json:"org"`
	Workspace         string          `json:"workspace"`
	EntityType        string          `json:"entity_type"`
	EntityID          string          `json:"entity_id"`
	EventName         string          `json:"event_name"`
	EventID           string          `json:"event_id"`
	SupersedesEventID string          `json:"supersedes_event_id,omitempty"`
	Data              json.RawMessage `json:"data,omitempty"`
	Seq               uint64          `json:"seq"`
	OccurredAt        time.Time       `json:"occurred_at"`
	ReceivedAt        time.Time       `json:"received_at"`
	RecordedAt        time.Time       `json:"recorded_at"`
	Status            string          `json:"status"`
	SupersededBy      string          `json:"superseded_by,omitempty"`
	SupersededAt      *time.Time      `json:"superseded_at,omitempty"`
	RetractedAt       *time.Time      `json:"retracted_at,omitempty"`
	RetractionReason  string          `json:"retraction_reason,omitempty"`
	SchemaVersion     int             `json:"schema_version,omitempty"`
	SchemaHash        string          `json:"schema_hash,omitempty"`
}

type eventIndex struct {
	EventKey string `json:"event_key"`
}

// Projector folds context events into entity + event documents.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "context" }

// Collections lists the store collections this projector owns.
func (Projector) Collections() []string {
	return []string{
		CollectionEntities, CollectionEntityHistory, CollectionEvents, CollectionEventIndex,
	}
}

// Apply maintains the entity document and the per-entity event log.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeEntityRecorded:
		return applyEntity(ctx, e, s)
	case events.TypeEventRecorded:
		return applyEvent(ctx, e, s)
	case events.TypeEventRetracted:
		return applyRetraction(ctx, e, s)
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
		if p.SchemaEvidence.Version > 0 {
			c.SchemaVersion, c.SchemaHash = p.SchemaEvidence.Version, p.SchemaEvidence.Hash
		}
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
		EventID: p.EventID, SupersedesEventID: p.SupersedesEventID,
		Data: p.Data, Seq: e.Seq, OccurredAt: p.OccurredAt,
		ReceivedAt: p.ReceivedAt, RecordedAt: e.Time, Status: "active",
		SchemaVersion: p.SchemaEvidence.Version, SchemaHash: p.SchemaEvidence.Hash,
	}
	if ev.EventID == "" {
		ev.EventID = e.ID
	}
	if ev.ReceivedAt.IsZero() {
		ev.ReceivedAt = e.Time
	}
	if p.SupersedesEventID != "" {
		previous, found, err := ReadEvent(ctx, s, identity.Identity{
			Org: e.Org, Workspace: e.Workspace, Actor: e.Actor,
		}, p.SupersedesEventID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("context-layer: correction seq %d supersedes unknown event %q", e.Seq, p.SupersedesEventID)
		}
		if previous.Status != "active" {
			return fmt.Errorf("context-layer: correction seq %d supersedes non-active event %q", e.Seq, p.SupersedesEventID)
		}
		previous.Status, previous.SupersededBy = "superseded", ev.EventID
		supersededAt := ev.ReceivedAt
		previous.SupersededAt = &supersededAt
		if err := putEvent(ctx, s, previous); err != nil {
			return err
		}
	}
	key := eventKey(e.Org, e.Workspace, p.EntityType, p.EntityID, e.Seq)
	if err := store.PutDoc(ctx, s, CollectionEvents, key, ev); err != nil {
		return err
	}
	if err := store.PutDoc(ctx, s, CollectionEventIndex,
		store.Key(e.Org, e.Workspace, ev.EventID), eventIndex{EventKey: key}); err != nil {
		return err
	}
	// Recording an event about an entity also touches the entity (auto-creating a
	// shell when the event arrives before any explicit RecordEntity).
	return upsertEntity(ctx, s, e, p.EntityType, p.EntityID, func(c *EntityView) error {
		c.EventCount++
		return nil
	})
}

func applyRetraction(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var payload events.EventRetracted
	if err := decode(e, &payload); err != nil {
		return err
	}
	ev, found, err := ReadEvent(ctx, s, identity.Identity{
		Org: e.Org, Workspace: e.Workspace, Actor: e.Actor,
	}, payload.EventID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("context-layer: retraction seq %d targets unknown event %q", e.Seq, payload.EventID)
	}
	if ev.Status != "active" {
		return fmt.Errorf("context-layer: retraction seq %d targets non-active event %q", e.Seq, payload.EventID)
	}
	ev.Status, ev.RetractedAt, ev.RetractionReason = "retracted", &payload.RetractedAt, payload.Reason
	if err := putEvent(ctx, s, ev); err != nil {
		return err
	}
	return upsertEntity(ctx, s, e, ev.EntityType, ev.EntityID, func(entity *EntityView) error {
		entity.EventCount++
		return nil
	})
}

func putEvent(ctx context.Context, s store.Store, ev EventView) error {
	return store.PutDoc(
		ctx, s, CollectionEvents,
		eventKey(ev.Org, ev.Workspace, ev.EntityType, ev.EntityID, ev.Seq), ev,
	)
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
	if err := store.PutDoc(ctx, s, CollectionEntities, key, c); err != nil {
		return err
	}
	return store.PutDoc(
		ctx, s, CollectionEntityHistory,
		store.Key(
			e.Org, e.Workspace,
			fmt.Sprintf("%s/%020d", entKey(entityType, entityID), e.Seq),
		),
		EntityVersion{Entity: c, Seq: e.Seq},
	)
}

// ReadEntity returns one entity for id's tenant.
func ReadEntity(ctx context.Context, s store.Store, id identity.Identity, entityType, entityID string) (EntityView, bool, error) {
	return store.GetDoc[EntityView](ctx, s, CollectionEntities, store.Key(id.Org, id.Workspace, entKey(entityType, entityID)))
}

// ListEntities returns the tenant's entities, optionally filtered by type, most
// recently updated first.
func ListEntities(ctx context.Context, s store.Store, id identity.Identity, entityType string) ([]EntityView, error) {
	return store.QueryDocs(ctx, s, CollectionEntities, store.Key(id.Org, id.Workspace, ""),
		func(c EntityView) bool { return entityType == "" || c.EntityType == entityType },
		func(a, b EntityView) bool { return a.UpdatedAt.After(b.UpdatedAt) })
}

// ListEntitiesAt returns the last entity state known no later than knowledgeAt.
// The history is a replay-owned projection, so pre-upgrade events gain the same
// semantics after rebuild without mutating the append-only log.
func ListEntitiesAt(
	ctx context.Context,
	s store.Store,
	id identity.Identity,
	entityType string,
	knowledgeAt time.Time,
) ([]EntityView, error) {
	if knowledgeAt.IsZero() {
		return nil, fmt.Errorf("context-layer: knowledge_at is required")
	}
	versions, err := store.ListDocs[EntityVersion](
		ctx, s, CollectionEntityHistory, store.Key(id.Org, id.Workspace, ""),
	)
	if err != nil {
		return nil, err
	}
	latest := make(map[string]EntityVersion)
	for _, version := range versions {
		entity := version.Entity
		if entity.EntityType != entityType || entity.UpdatedAt.After(knowledgeAt) {
			continue
		}
		key := entKey(entity.EntityType, entity.EntityID)
		if current, exists := latest[key]; !exists || version.Seq > current.Seq {
			latest[key] = version
		}
	}
	result := make([]EntityView, 0, len(latest))
	for _, version := range latest {
		result = append(result, version.Entity)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].EntityID < result[right].EntityID
	})
	return result, nil
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

// ReadEvent resolves one durable source event identity.
func ReadEvent(
	ctx context.Context,
	s store.Store,
	id identity.Identity,
	eventID string,
) (EventView, bool, error) {
	index, found, err := store.GetDoc[eventIndex](
		ctx, s, CollectionEventIndex, store.Key(id.Org, id.Workspace, eventID),
	)
	if err != nil || !found {
		return EventView{}, found, err
	}
	return store.GetDoc[EventView](ctx, s, CollectionEvents, index.EventKey)
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
