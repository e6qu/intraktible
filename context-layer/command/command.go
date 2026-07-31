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
	"errors"
	"fmt"
	"io"
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

// WithNow overrides the clock used to stamp recorded events (deterministic
// tests, the demo seeder) and returns the handler.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
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
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	var folded contextFoldState
	if cmd.SchemaEvidence.UniqueClaim != "" || len(cmd.SchemaEvidence.RelationshipChecks) > 0 {
		var err error
		folded, err = h.foldContextState(ctx, id)
		if err != nil {
			return eventlog.Envelope{}, err
		}
	}
	owner := entityFactKey(cmd.EntityType, cmd.EntityID)
	if folded.entities == nil {
		folded.entities = map[string]bool{}
	}
	folded.entities[owner] = true
	evidence, err := evaluateRelationships(cmd.SchemaEvidence, folded.entities)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	payload := events.EntityRecorded{
		EntityType:     cmd.EntityType,
		EntityID:       cmd.EntityID,
		Attributes:     cmd.Attributes,
		SchemaEvidence: schemaEvidence(evidence),
	}
	if evidence.UniqueClaim == "" {
		return h.append(ctx, id, events.TypeEntityRecorded, payload)
	}
	if folded.uniqueOwners[evidence.UniqueClaim] == owner {
		return h.append(ctx, id, events.TypeEntityRecorded, payload)
	}
	envelope, err := eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, events.StreamContext,
		events.TypeEntityRecorded, h.now(), payload,
		sourceUniqueEnvelopeClaim(id, evidence.UniqueClaim),
	)
	if !errors.Is(err, eventlog.ErrConflict) {
		return envelope, err
	}
	// Another replica may have claimed the tuple after our fold. Re-read before
	// deciding whether this is an idempotent update by the same entity or a real
	// cross-entity quality violation.
	folded, err = h.foldContextState(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if folded.uniqueOwners[evidence.UniqueClaim] == owner {
		return h.append(ctx, id, events.TypeEntityRecorded, payload)
	}
	evidence.Violations = append(evidence.Violations, domain.QualityViolation{
		Code: "unique", Message: "entity uniqueness tuple is already owned by another entity",
	})
	if evidence.Action == "block" {
		return eventlog.Envelope{}, fmt.Errorf(
			"context-layer: entity %s/%s rejected by uniqueness contract",
			cmd.EntityType, cmd.EntityID,
		)
	}
	payload.SchemaEvidence = schemaEvidence(evidence)
	return h.append(ctx, id, events.TypeEntityRecorded, payload)
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
	var folded contextFoldState
	if cmd.SupersedesEventID != "" || len(cmd.SchemaEvidence.RelationshipChecks) > 0 {
		var err error
		folded, err = h.foldContextState(ctx, id)
		if err != nil {
			return eventlog.Envelope{}, err
		}
	}
	evidence, err := evaluateRelationships(cmd.SchemaEvidence, folded.entities)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	recordedAt := h.now()
	occurredAt := cmd.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = recordedAt
	}
	eventID := cmd.EventID
	if eventID == "" {
		eventID = newID()
	}
	claim := "context.event.id\x00" + eventID
	if cmd.SupersedesEventID != "" {
		target, ok := folded.sourceEvents[cmd.SupersedesEventID]
		if !ok {
			return eventlog.Envelope{}, fmt.Errorf(
				"context-layer: cannot supersede unknown event %q", cmd.SupersedesEventID,
			)
		}
		if !target.active {
			return eventlog.Envelope{}, fmt.Errorf(
				"context-layer: event %q is already superseded or retracted", cmd.SupersedesEventID,
			)
		}
		if target.payload.EntityType != cmd.EntityType ||
			target.payload.EntityID != cmd.EntityID ||
			target.payload.EventName != cmd.EventName {
			return eventlog.Envelope{}, fmt.Errorf(
				"context-layer: correction coordinates must match event %q", cmd.SupersedesEventID,
			)
		}
		claim = "context.event.terminal\x00" + cmd.SupersedesEventID
	}
	return eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, events.StreamContext,
		events.TypeEventRecorded, recordedAt, events.EventRecorded{
			EntityType:        cmd.EntityType,
			EntityID:          cmd.EntityID,
			EventName:         cmd.EventName,
			EventID:           eventID,
			SupersedesEventID: cmd.SupersedesEventID,
			Data:              cmd.Data,
			OccurredAt:        occurredAt,
			ReceivedAt:        recordedAt,
			SchemaEvidence:    schemaEvidence(evidence),
		}, id.Org+"\x00"+id.Workspace+"\x00"+claim)
}

// RetractEvent records a terminal retraction edge for one source event.
func (h *Handler) RetractEvent(
	ctx context.Context,
	id identity.Identity,
	cmd domain.RetractEvent,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	state, err := h.foldSourceEvents(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	target, ok := state[cmd.EventID]
	if !ok {
		return eventlog.Envelope{}, fmt.Errorf("context-layer: cannot retract unknown event %q", cmd.EventID)
	}
	if !target.active {
		return eventlog.Envelope{}, fmt.Errorf(
			"context-layer: event %q is already superseded or retracted", cmd.EventID,
		)
	}
	at := h.now()
	envelope, err := eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, events.StreamContext,
		events.TypeEventRetracted, at,
		events.EventRetracted{
			EventID: cmd.EventID, EntityType: target.payload.EntityType,
			EntityID: target.payload.EntityID, EventName: target.payload.EventName,
			Reason: cmd.Reason, RetractedAt: at,
		},
		id.Org+"\x00"+id.Workspace+"\x00context.event.terminal\x00"+cmd.EventID,
	)
	if errors.Is(err, eventlog.ErrConflict) {
		return eventlog.Envelope{}, fmt.Errorf(
			"context-layer: event %q was concurrently superseded or retracted: %w", cmd.EventID, err,
		)
	}
	return envelope, err
}

type sourceEventState struct {
	payload events.EventRecorded
	active  bool
}

type contextFoldState struct {
	sourceEvents map[string]sourceEventState
	entities     map[string]bool
	uniqueOwners map[string]string
}

func entityFactKey(entityType, entityID string) string {
	return entityType + "\x00" + entityID
}

func sourceUniqueEnvelopeClaim(id identity.Identity, claim string) string {
	return id.Org + "\x00" + id.Workspace + "\x00context." + claim
}

func evaluateRelationships(
	evidence domain.SchemaEvidence,
	entities map[string]bool,
) (domain.SchemaEvidence, error) {
	for _, relationship := range evidence.RelationshipChecks {
		if entities[entityFactKey(relationship.TargetEntityType, relationship.TargetEntityID)] {
			continue
		}
		evidence.Violations = append(evidence.Violations, domain.QualityViolation{
			Code: "relationship", Field: relationship.Field,
			Message: fmt.Sprintf(
				"relationship field %q targets unknown %s entity",
				relationship.Field, relationship.TargetEntityType,
			),
		})
		if evidence.Action == "block" {
			return domain.SchemaEvidence{}, fmt.Errorf(
				"context-layer: relationship field %q targets unknown %s/%s",
				relationship.Field, relationship.TargetEntityType,
				relationship.TargetEntityID,
			)
		}
	}
	evidence.RelationshipChecks = nil
	return evidence, nil
}

func (h *Handler) foldSourceEvents(
	ctx context.Context,
	id identity.Identity,
) (map[string]sourceEventState, error) {
	state, err := h.foldContextState(ctx, id)
	return state.sourceEvents, err
}

func (h *Handler) foldContextState(
	ctx context.Context,
	id identity.Identity,
) (contextFoldState, error) {
	envelopes, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, events.StreamContext, 0)
	if err != nil {
		return contextFoldState{}, err
	}
	state := contextFoldState{
		sourceEvents: make(map[string]sourceEventState),
		entities:     make(map[string]bool),
		uniqueOwners: make(map[string]string),
	}
	for _, envelope := range envelopes {
		switch envelope.Type {
		case events.TypeEntityRecorded:
			var payload events.EntityRecorded
			if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
				return contextFoldState{}, fmt.Errorf(
					"context-layer: decode entity seq %d: %w", envelope.Seq, err,
				)
			}
			owner := entityFactKey(payload.EntityType, payload.EntityID)
			state.entities[owner] = true
			if claim := payload.SchemaEvidence.UniqueClaim; claim != "" {
				if _, exists := state.uniqueOwners[claim]; !exists {
					state.uniqueOwners[claim] = owner
				}
			}
		case events.TypeEventRecorded:
			var payload events.EventRecorded
			if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
				return contextFoldState{}, fmt.Errorf(
					"context-layer: decode event seq %d: %w", envelope.Seq, err,
				)
			}
			eventID := payload.EventID
			if eventID == "" {
				eventID = envelope.ID
				payload.EventID = eventID
			}
			if payload.SupersedesEventID != "" {
				target := state.sourceEvents[payload.SupersedesEventID]
				target.active = false
				state.sourceEvents[payload.SupersedesEventID] = target
			}
			state.sourceEvents[eventID] = sourceEventState{payload: payload, active: true}
		case events.TypeEventRetracted:
			var payload events.EventRetracted
			if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
				return contextFoldState{}, fmt.Errorf(
					"context-layer: decode retraction seq %d: %w", envelope.Seq, err,
				)
			}
			target := state.sourceEvents[payload.EventID]
			target.active = false
			state.sourceEvents[payload.EventID] = target
		}
	}
	return state, nil
}

func schemaEvidence(evidence domain.SchemaEvidence) events.SchemaEvidence {
	violations := make([]events.QualityViolation, len(evidence.Violations))
	for index, violation := range evidence.Violations {
		violations[index] = events.QualityViolation{
			Code: violation.Code, Field: violation.Field, Message: violation.Message,
		}
	}
	return events.SchemaEvidence{
		Version: evidence.Version, Hash: evidence.Hash, Action: evidence.Action,
		Violations: violations, EvaluatedAt: evidence.EvaluatedAt,
		PolicyApprovalID:     evidence.PolicyApprovalID,
		PolicyApprovedBy:     evidence.PolicyApprovedBy,
		PolicyApprovedAt:     evidence.PolicyApprovedAt,
		PolicyApprovalReason: evidence.PolicyApprovalReason,
		UniqueClaim:          evidence.UniqueClaim,
	}
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
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic("context-layer: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
