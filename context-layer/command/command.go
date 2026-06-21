// SPDX-License-Identifier: AGPL-3.0-or-later

// Package command is the Context Layer's write side (imperative shell): it
// validates via the functional core, then appends events to the log. Entities are
// upserts and events may precede their entity, so no existence check is needed.
package command

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

// validator is implemented by every domain command.
type validator interface{ Validate() error }

// emit checks the caller and validates the command, then appends the event —
// the shared validate→append spine of the write side.
func (h *Handler) emit(ctx context.Context, id identity.Identity, cmd validator, typ string, payload any) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	return h.append(ctx, id, typ, payload)
}

// RecordEntity upserts a custom entity.
func (h *Handler) RecordEntity(ctx context.Context, id identity.Identity, cmd domain.RecordEntity) (eventlog.Envelope, error) {
	return h.emit(ctx, id, cmd, events.TypeEntityRecorded, events.EntityRecorded{
		EntityType: cmd.EntityType,
		EntityID:   cmd.EntityID,
		Attributes: cmd.Attributes,
	})
}

// RecordEvent records a custom event about an entity, filling OccurredAt with the
// record time when the caller omits it (a recorded effect, replay-stable).
func (h *Handler) RecordEvent(ctx context.Context, id identity.Identity, cmd domain.RecordEvent) (eventlog.Envelope, error) {
	occurredAt := cmd.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = h.now()
	}
	return h.emit(ctx, id, cmd, events.TypeEventRecorded, events.EventRecorded{
		EntityType: cmd.EntityType,
		EntityID:   cmd.EntityID,
		EventName:  cmd.EventName,
		Data:       cmd.Data,
		OccurredAt: occurredAt,
	})
}

// DefineFeature defines (or redefines) a windowed feature over an entity type's
// event stream.
func (h *Handler) DefineFeature(ctx context.Context, id identity.Identity, cmd domain.DefineFeature) (eventlog.Envelope, error) {
	return h.emit(ctx, id, cmd, events.TypeFeatureDefined, events.FeatureDefined{
		Name:        cmd.Name,
		EntityType:  cmd.EntityType,
		EventName:   cmd.EventName,
		Aggregation: string(cmd.Aggregation),
		Field:       cmd.Field,
		WindowHours: cmd.WindowHours,
	})
}

// DefineConnector registers (or redefines) a named external connector.
func (h *Handler) DefineConnector(ctx context.Context, id identity.Identity, cmd domain.DefineConnector) (eventlog.Envelope, error) {
	return h.emit(ctx, id, cmd, events.TypeConnectorDefined, events.ConnectorDefined{
		Name: cmd.Name, Type: string(cmd.Type), Config: cmd.Config,
	})
}

// RecordFetch records a connector invocation and its result. The fetch itself
// (the external call) is performed by the shell; recording the response here is
// what makes the result auditable and replay-stable. It returns the fetch id.
func (h *Handler) RecordFetch(ctx context.Context, id identity.Identity, connector string, params, response json.RawMessage) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	fetchID := newID()
	e, err := h.append(ctx, id, events.TypeConnectorFetched, events.ConnectorFetched{
		FetchID: fetchID, Connector: connector, Params: params, Response: response, At: h.now(),
	})
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	return fetchID, e, nil
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ string, payload any) (eventlog.Envelope, error) {
	return eventlog.AppendJSON(ctx, h.log, id.Org, id.Workspace, id.Actor, events.StreamContext, typ, h.now(), payload)
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
