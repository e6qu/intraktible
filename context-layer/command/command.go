// SPDX-License-Identifier: AGPL-3.0-or-later

// Package command is the Context Layer's write side (imperative shell): it
// validates via the functional core, then appends events to the log. Entities are
// upserts and events may precede their entity, so no existence check is needed.
package command

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/context-layer/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// Handler records Context Layer events.
type Handler struct {
	log eventlog.Log
	now func() time.Time
}

// NewHandler builds a Handler using the system clock.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }}
}

// RecordEntity upserts a custom entity.
func (h *Handler) RecordEntity(ctx context.Context, id identity.Identity, cmd domain.RecordEntity) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	return h.append(ctx, id, events.TypeEntityRecorded, events.EntityRecorded{
		EntityType: cmd.EntityType,
		EntityID:   cmd.EntityID,
		Attributes: cmd.Attributes,
	})
}

// RecordEvent records a custom event about an entity, filling OccurredAt with the
// record time when the caller omits it (a recorded effect, replay-stable).
func (h *Handler) RecordEvent(ctx context.Context, id identity.Identity, cmd domain.RecordEvent) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	occurredAt := cmd.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = h.now()
	}
	return h.append(ctx, id, events.TypeEventRecorded, events.EventRecorded{
		EntityType: cmd.EntityType,
		EntityID:   cmd.EntityID,
		EventName:  cmd.EventName,
		Data:       cmd.Data,
		OccurredAt: occurredAt,
	})
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ string, payload any) (eventlog.Envelope, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("context-layer: marshal %s: %w", typ, err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org:       id.Org,
		Workspace: id.Workspace,
		Actor:     id.Actor,
		Stream:    events.StreamContext,
		Type:      typ,
		Time:      h.now(),
		Payload:   b,
	})
}
