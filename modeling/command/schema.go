// SPDX-License-Identifier: AGPL-3.0-or-later

// Package command is the modeling write side. It folds immutable governance
// events, applies the pure domain rules, and appends the next fact.
package command

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/intraktible/modeling/domain"
	"github.com/e6qu/intraktible/modeling/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

const maxClaimRetries = 3

// Handler owns modeling governance commands.
type Handler struct {
	log eventlog.Log
	now func() time.Time
	mu  sync.Mutex
}

// NewHandler constructs a Handler with the system clock.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the command clock for deterministic tests and seed builds.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

type schemaState struct {
	versions       map[int]events.SchemaVersionDefined
	owners         map[int]string
	activeVersion  int
	pendingID      string
	pendingVersion int
	pendingBy      string
	retired        map[int]bool
}

func emptySchemaState() schemaState {
	return schemaState{
		versions: make(map[int]events.SchemaVersionDefined),
		owners:   make(map[int]string),
		retired:  make(map[int]bool),
	}
}

func (h *Handler) foldSchema(ctx context.Context, id identity.Identity, ref domain.SchemaRef) (schemaState, error) {
	envelopes, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, events.StreamModeling, 0)
	if err != nil {
		return schemaState{}, err
	}
	state := emptySchemaState()
	for _, envelope := range envelopes {
		switch envelope.Type {
		case events.TypeSchemaVersionDefined:
			var payload events.SchemaVersionDefined
			if err := decode(envelope, &payload); err != nil {
				return schemaState{}, err
			}
			if payload.Ref.Key() == ref.Key() {
				state.versions[payload.Version] = payload
				state.owners[payload.Version] = envelope.Actor
			}
		case events.TypeSchemaApprovalRequested:
			var payload events.SchemaApprovalRequested
			if err := decode(envelope, &payload); err != nil {
				return schemaState{}, err
			}
			if payload.Ref.Key() == ref.Key() {
				state.pendingID = payload.RequestID
				state.pendingVersion = payload.Version
				state.pendingBy = envelope.Actor
			}
		case events.TypeSchemaApprovalApproved:
			var payload events.SchemaApprovalApproved
			if err := decode(envelope, &payload); err != nil {
				return schemaState{}, err
			}
			if payload.Ref.Key() == ref.Key() && payload.RequestID == state.pendingID {
				state.activeVersion = payload.Version
				state.pendingID, state.pendingVersion, state.pendingBy = "", 0, ""
			}
		case events.TypeSchemaApprovalRejected:
			var payload events.SchemaApprovalRejected
			if err := decode(envelope, &payload); err != nil {
				return schemaState{}, err
			}
			if payload.Ref.Key() == ref.Key() && payload.RequestID == state.pendingID {
				state.pendingID, state.pendingVersion, state.pendingBy = "", 0, ""
			}
		case events.TypeSchemaVersionRetired:
			var payload events.SchemaVersionRetired
			if err := decode(envelope, &payload); err != nil {
				return schemaState{}, err
			}
			if payload.Ref.Key() == ref.Key() {
				state.retired[payload.Version] = true
				if state.activeVersion == payload.Version {
					state.activeVersion = 0
				}
			}
		}
	}
	return state, nil
}

// DefineSchema defines the next immutable version. Compatibility is checked
// against the active approved version, not an unapproved draft.
func (h *Handler) DefineSchema(
	ctx context.Context,
	id identity.Identity,
	spec domain.SchemaSpec,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := spec.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	hash, err := domain.HashSchema(spec)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; attempt < maxClaimRetries; attempt++ {
		state, err := h.foldSchema(ctx, id, spec.Ref)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		version := 1
		for candidate := range state.versions {
			if candidate >= version {
				version = candidate + 1
			}
		}
		var breaks []string
		if active, ok := state.versions[state.activeVersion]; ok {
			breaks = domain.CompatibilityBreaks(active.Spec, spec)
			if len(breaks) > 0 {
				return eventlog.Envelope{}, fmt.Errorf(
					"modeling: schema %s version %d is incompatible: %s",
					spec.Ref.Key(), version, strings.Join(breaks, "; "),
				)
			}
		}
		payload := events.SchemaVersionDefined{
			Ref: spec.Ref, Version: version, Spec: spec, Hash: hash,
			CompatibilityBreaks: breaks,
		}
		envelope, err := h.appendUnique(ctx, id, events.TypeSchemaVersionDefined, payload,
			"modeling.schema.version\x00"+spec.Ref.Key()+"\x00"+fmt.Sprint(version))
		if errors.Is(err, eventlog.ErrConflict) {
			continue
		}
		return envelope, err
	}
	return eventlog.Envelope{}, fmt.Errorf("modeling: concurrent schema definitions for %s did not settle", spec.Ref.Key())
}

// RequestSchemaApproval submits an immutable version to an independent checker.
func (h *Handler) RequestSchemaApproval(
	ctx context.Context,
	id identity.Identity,
	ref domain.SchemaRef,
	version int,
) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if err := ref.Validate(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.foldSchema(ctx, id, ref)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	if _, ok := state.versions[version]; !ok {
		return "", eventlog.Envelope{}, fmt.Errorf("modeling: unknown schema %s version %d", ref.Key(), version)
	}
	if state.retired[version] {
		return "", eventlog.Envelope{}, fmt.Errorf("modeling: schema %s version %d is retired", ref.Key(), version)
	}
	if state.activeVersion == version {
		return "", eventlog.Envelope{}, fmt.Errorf("modeling: schema %s version %d is already active", ref.Key(), version)
	}
	if state.pendingID != "" {
		return "", eventlog.Envelope{}, fmt.Errorf("modeling: schema %s already has pending request %q", ref.Key(), state.pendingID)
	}
	requestID := newID()
	envelope, err := h.appendUnique(ctx, id, events.TypeSchemaApprovalRequested,
		events.SchemaApprovalRequested{RequestID: requestID, Ref: ref, Version: version},
		"modeling.schema.request\x00"+ref.Key()+"\x00"+fmt.Sprint(version))
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	return requestID, envelope, nil
}

// DecideSchemaApproval approves or rejects the current pending request.
func (h *Handler) DecideSchemaApproval(
	ctx context.Context,
	id identity.Identity,
	ref domain.SchemaRef,
	requestID string,
	approve bool,
	reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := ref.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(requestID) == "" {
		return eventlog.Envelope{}, errors.New("modeling: request_id is required")
	}
	if strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, errors.New("modeling: schema approval decision reason is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; attempt < maxClaimRetries; attempt++ {
		state, err := h.foldSchema(ctx, id, ref)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		if state.pendingID != requestID {
			return eventlog.Envelope{}, fmt.Errorf("modeling: no pending request %q for schema %s", requestID, ref.Key())
		}
		version := state.pendingVersion
		if approve {
			if id.Actor == state.pendingBy {
				return eventlog.Envelope{}, fmt.Errorf("modeling: four-eyes — %q cannot approve their own schema request", id.Actor)
			}
			if id.Actor == state.owners[version] {
				return eventlog.Envelope{}, fmt.Errorf("modeling: four-eyes — %q authored schema %s version %d", id.Actor, ref.Key(), version)
			}
		}
		typ := events.TypeSchemaApprovalApproved
		var payload any = events.SchemaApprovalApproved{
			RequestID: requestID, Ref: ref, Version: version, Reason: strings.TrimSpace(reason),
		}
		if !approve {
			typ = events.TypeSchemaApprovalRejected
			payload = events.SchemaApprovalRejected{
				RequestID: requestID, Ref: ref, Version: version, Reason: strings.TrimSpace(reason),
			}
		}
		envelope, err := h.appendUnique(ctx, id, typ, payload,
			"modeling.schema.decision\x00"+ref.Key()+"\x00"+requestID)
		if errors.Is(err, eventlog.ErrConflict) {
			continue
		}
		return envelope, err
	}
	return eventlog.Envelope{}, fmt.Errorf("modeling: concurrent decision for schema %s request %s did not settle", ref.Key(), requestID)
}

// RetireSchema retires an approved or superseded schema version. Retiring the
// active version leaves the source without an active governed contract.
func (h *Handler) RetireSchema(
	ctx context.Context,
	id identity.Identity,
	ref domain.SchemaRef,
	version int,
	reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := ref.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, errors.New("modeling: retirement reason is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.foldSchema(ctx, id, ref)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if _, ok := state.versions[version]; !ok {
		return eventlog.Envelope{}, fmt.Errorf("modeling: unknown schema %s version %d", ref.Key(), version)
	}
	if state.retired[version] {
		return eventlog.Envelope{}, fmt.Errorf("modeling: schema %s version %d is already retired", ref.Key(), version)
	}
	return h.appendUnique(ctx, id, events.TypeSchemaVersionRetired, events.SchemaVersionRetired{
		Ref: ref, Version: version, Reason: strings.TrimSpace(reason), RetiredAt: h.now(),
	}, "modeling.schema.retire\x00"+ref.Key()+"\x00"+fmt.Sprint(version))
}

func (h *Handler) appendUnique(
	ctx context.Context,
	id identity.Identity,
	typ string,
	payload any,
	claim string,
) (eventlog.Envelope, error) {
	return eventlog.AppendClaim(ctx, h.log, id, events.StreamModeling, typ, h.now(), payload, claim)
}

func decode(envelope eventlog.Envelope, payload any) error {
	if err := json.Unmarshal(envelope.Payload, payload); err != nil {
		return fmt.Errorf("modeling: decode %s seq %d: %w", envelope.Type, envelope.Seq, err)
	}
	return nil
}

func newID() string {
	var value [16]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		panic("modeling: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(value[:])
}
