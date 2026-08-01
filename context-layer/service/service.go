// SPDX-License-Identifier: AGPL-3.0-or-later

// Package service is the Context Layer's HTTP surface (imperative shell): record
// custom entities and events, and read the entity store + per-entity event log.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/intraktible/context-layer/command"
	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/context-layer/events"
	"github.com/e6qu/intraktible/context-layer/features"
	"github.com/e6qu/intraktible/platform/erasure"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Service wires the Context Layer commands and read model to HTTP.
type Service struct {
	cmd     *command.Handler
	store   store.Store
	egress  connectors.EgressPolicy
	secrets *connectors.Keyring
	// eraser crypto-shreds the named PII fields of custom-event data, sealed per
	// entity subject; piiFields is the set to seal.
	eraser    *erasure.Vault
	piiFields map[string]bool
	now       func() time.Time
	contracts ContractValidator
}

// ContractValidator is the Context Layer's schema-governance port. The
// modeling adapter implements it without making Context's functional core
// import the modeling bounded context.
type ContractValidator interface {
	ValidateEntity(
		context.Context,
		identity.Identity,
		string,
		int,
		json.RawMessage,
	) (domain.SchemaEvidence, error)
	ValidateEvent(
		context.Context,
		identity.Identity,
		string,
		string,
		time.Time,
		int,
		json.RawMessage,
	) (domain.SchemaEvidence, error)
}

// Option configures a Service.
type Option func(*Service)

// WithNow overrides the clock feature computation reads (deterministic tests,
// the demo seeder).
func WithNow(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithContracts enables governed source validation.
func WithContracts(validator ContractValidator) Option {
	return func(s *Service) { s.contracts = validator }
}

// WithEgress sets the HTTP connector's egress policy (SSRF guard). The default
// (zero value) blocks loopback/private targets.
func WithEgress(p connectors.EgressPolicy) Option {
	return func(s *Service) { s.egress = p }
}

// WithSecrets enables connector credential encryption/decryption at the HTTP
// boundary. Credential fields are encrypted before ConnectorDefined is emitted.
func WithSecrets(kr *connectors.Keyring) Option {
	return func(s *Service) { s.secrets = kr }
}

// WithErasure crypto-shreds the named PII fields of custom-event data: each is
// sealed under its entity subject before the event is recorded, and opened (or
// shown "[erased]" once the subject is erased) on read. Only the named fields
// are sealed, so the feature engine and other readers of non-PII fields are
// unaffected.
func WithErasure(v *erasure.Vault, fields []string) Option {
	return func(s *Service) {
		if v == nil || len(fields) == 0 {
			return
		}
		s.eraser = v
		s.piiFields = make(map[string]bool, len(fields))
		for _, f := range fields {
			s.piiFields[f] = true
		}
	}
}

// New builds the service.
func New(cmd *command.Handler, st store.Store, opts ...Option) *Service {
	s := &Service{cmd: cmd, store: st, now: func() time.Time { return time.Now().UTC() }}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Routes registers the Context Layer endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/context/entities", s.recordEntity)
	mux.HandleFunc("POST /v1/context/entities/bulk", s.bulkEntities)
	mux.HandleFunc("GET /v1/context/entities", s.listEntities)
	mux.HandleFunc("GET /v1/context/entities/{type}/{id}", s.getEntity)
	mux.HandleFunc("GET /v1/context/entities/{type}/{id}/events", s.listEvents)
	mux.HandleFunc("GET /v1/context/entities/{type}/{id}/features", s.computeFeatures)
	mux.HandleFunc("POST /v1/context/events", s.recordEvent)
	mux.HandleFunc("POST /v1/context/events/bulk", s.bulkEvents)
	mux.HandleFunc("GET /v1/context/events/{event_id}", s.getEvent)
	mux.HandleFunc("POST /v1/context/events/{event_id}/retract", s.retractEvent)
	mux.HandleFunc("POST /v1/context/features", s.defineFeature)
	mux.HandleFunc("GET /v1/context/features", s.listFeatures)
	mux.HandleFunc("POST /v1/context/connectors", s.defineConnector)
	mux.HandleFunc("GET /v1/context/connectors", s.listConnectors)
	mux.HandleFunc("GET /v1/context/connectors/catalog", s.connectorCatalog)
	mux.HandleFunc("POST /v1/context/connectors/{name}/fetch", s.fetchConnector)
	mux.HandleFunc("GET /v1/context/connectors/{name}/fetches", s.listFetches)
}

type entityRequest struct {
	EntityType    string          `json:"entity_type"`
	EntityID      string          `json:"entity_id"`
	SchemaVersion int             `json:"schema_version,omitempty"`
	Attributes    json.RawMessage `json:"attributes,omitempty"`
}

func (s *Service) recordEntity(w http.ResponseWriter, r *http.Request) {
	var req entityRequest
	httpx.Emit(w, r, &req, func(id identity.Identity) (eventlog.Envelope, error) {
		var evidence domain.SchemaEvidence
		if s.contracts != nil {
			var err error
			evidence, err = s.contracts.ValidateEntity(
				r.Context(), id, req.EntityType, req.SchemaVersion, req.Attributes,
			)
			if err != nil {
				return eventlog.Envelope{}, err
			}
		}
		return s.cmd.RecordEntity(r.Context(), id, domain.RecordEntity{
			EntityType: req.EntityType, EntityID: req.EntityID, Attributes: req.Attributes,
			SchemaEvidence: evidence,
		})
	})
}

type eventRequest struct {
	EntityType        string          `json:"entity_type"`
	EntityID          string          `json:"entity_id"`
	EventName         string          `json:"event_name"`
	EventID           string          `json:"event_id,omitempty"`
	SupersedesEventID string          `json:"supersedes_event_id,omitempty"`
	SchemaVersion     int             `json:"schema_version,omitempty"`
	Data              json.RawMessage `json:"data,omitempty"`
	OccurredAt        time.Time       `json:"occurred_at,omitempty"`
}

func (s *Service) recordEvent(w http.ResponseWriter, r *http.Request) {
	var req eventRequest
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	occurredAt := req.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = s.now()
	}
	data := req.Data
	var evidence domain.SchemaEvidence
	if s.contracts != nil {
		var err error
		evidence, err = s.contracts.ValidateEvent(
			r.Context(), id, req.EntityType, req.EventName, occurredAt, req.SchemaVersion, data,
		)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err)
			return
		}
	}
	if s.eraser != nil {
		sealed, err := s.eraser.SealFields(
			r.Context(), id, eventSubject(req.EntityType, req.EntityID), data, s.piiFields,
		)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err)
			return
		}
		data = sealed
	}
	envelope, err := s.cmd.RecordEvent(r.Context(), id, domain.RecordEvent{
		EntityType: req.EntityType, EntityID: req.EntityID, EventName: req.EventName,
		EventID: req.EventID, SupersedesEventID: req.SupersedesEventID,
		Data: data, OccurredAt: occurredAt, SchemaEvidence: evidence,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	var recorded events.EventRecorded
	if err := json.Unmarshal(envelope.Payload, &recorded); err != nil {
		httpx.Error(w, http.StatusInternalServerError, fmt.Errorf(
			"context-layer: decode just-recorded event: %w", err,
		))
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"event_id": envelope.ID, "seq": envelope.Seq, "source_event_id": recorded.EventID,
	})
}

type retractEventRequest struct {
	Reason string `json:"reason"`
}

const maxBulkBatch = 1000

type bulkEntityRequest struct {
	Entities []entityRequest `json:"entities"`
}

type bulkResult struct {
	Index  int    `json:"index"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Seq    uint64 `json:"seq,omitempty"`
}

// bulkEntities ingests a bounded batch of entities. Each record is processed
// independently: a validation failure on one does not abort the rest. A 207
// Multi-Status response reports per-record results.
func (s *Service) bulkEntities(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req bulkEntityRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Entities) > maxBulkBatch {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf(
			"context: bulk entity batch %d exceeds the %d limit", len(req.Entities), maxBulkBatch,
		))
		return
	}
	results := make([]bulkResult, len(req.Entities))
	succeeded := 0
	for i, entity := range req.Entities {
		var evidence domain.SchemaEvidence
		if s.contracts != nil {
			ev, err := s.contracts.ValidateEntity(
				r.Context(), id, entity.EntityType, entity.SchemaVersion, entity.Attributes,
			)
			if err != nil {
				results[i] = bulkResult{Index: i, Status: "error", Error: err.Error()}
				continue
			}
			evidence = ev
		}
		envelope, err := s.cmd.RecordEntity(r.Context(), id, domain.RecordEntity{
			EntityType: entity.EntityType, EntityID: entity.EntityID,
			Attributes: entity.Attributes, SchemaEvidence: evidence,
		})
		if err != nil {
			results[i] = bulkResult{Index: i, Status: "error", Error: err.Error()}
			continue
		}
		results[i] = bulkResult{Index: i, Status: "ok", Seq: envelope.Seq}
		succeeded++
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"total":     len(req.Entities),
		"succeeded": succeeded,
		"failed":    len(req.Entities) - succeeded,
		"results":   results,
	})
}

type bulkEventRequest struct {
	Events []eventRequest `json:"events"`
}

// bulkEvents ingests a bounded batch of events with the same per-record
// independent processing as bulkEntities.
func (s *Service) bulkEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req bulkEventRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Events) > maxBulkBatch {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf(
			"context: bulk event batch %d exceeds the %d limit", len(req.Events), maxBulkBatch,
		))
		return
	}
	results := make([]bulkResult, len(req.Events))
	succeeded := 0
	for i, evt := range req.Events {
		occurredAt := evt.OccurredAt
		if occurredAt.IsZero() {
			occurredAt = s.now()
		}
		data := evt.Data
		var evidence domain.SchemaEvidence
		if s.contracts != nil {
			ev, err := s.contracts.ValidateEvent(
				r.Context(), id, evt.EntityType, evt.EventName, occurredAt, evt.SchemaVersion, data,
			)
			if err != nil {
				results[i] = bulkResult{Index: i, Status: "error", Error: err.Error()}
				continue
			}
			evidence = ev
		}
		if s.eraser != nil {
			sealed, err := s.eraser.SealFields(
				r.Context(), id, eventSubject(evt.EntityType, evt.EntityID), data, s.piiFields,
			)
			if err != nil {
				results[i] = bulkResult{Index: i, Status: "error", Error: err.Error()}
				continue
			}
			data = sealed
		}
		envelope, err := s.cmd.RecordEvent(r.Context(), id, domain.RecordEvent{
			EntityType: evt.EntityType, EntityID: evt.EntityID, EventName: evt.EventName,
			EventID: evt.EventID, SupersedesEventID: evt.SupersedesEventID,
			Data: data, OccurredAt: occurredAt, SchemaEvidence: evidence,
		})
		if err != nil {
			results[i] = bulkResult{Index: i, Status: "error", Error: err.Error()}
			continue
		}
		results[i] = bulkResult{Index: i, Status: "ok", Seq: envelope.Seq}
		succeeded++
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"total":     len(req.Events),
		"succeeded": succeeded,
		"failed":    len(req.Events) - succeeded,
		"results":   results,
	})
}

func (s *Service) retractEvent(w http.ResponseWriter, r *http.Request) {
	var request retractEventRequest
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.RetractEvent(r.Context(), id, domain.RetractEvent{
			EventID: r.PathValue("event_id"), Reason: request.Reason,
		})
	})
}

func (s *Service) getEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	event, found, err := entities.ReadEvent(r.Context(), s.store, id, r.PathValue("event_id"))
	httpx.WriteOne(w, event, found, err, "event not found")
}

// eventSubject is the erasure subject for an entity's events.
func eventSubject(entityType, entityID string) string { return entityType + "/" + entityID }

func (s *Service) listEntities(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	recs, err := entities.ListEntities(r.Context(), s.store, id, r.URL.Query().Get("type"))
	httpx.WritePage(w, r, "entities", recs, err)
}

func (s *Service) getEntity(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	c, found, err := entities.ReadEntity(r.Context(), s.store, id, r.PathValue("type"), r.PathValue("id"))
	httpx.WriteOne(w, c, found, err, "entity not found")
}

func (s *Service) listEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	recs, err := entities.ListEvents(r.Context(), s.store, id, r.PathValue("type"), r.PathValue("id"))
	if err == nil && s.eraser != nil {
		// Unseal crypto-shredded PII. An erased subject yields "[erased]" inside
		// OpenFields (not an error), so a non-nil error is a genuine vault/decrypt
		// fault — surface it rather than serving the raw sealed envelope.
		for i := range recs {
			opened, oerr := s.eraser.OpenFields(r.Context(), id, eventSubject(recs[i].EntityType, recs[i].EntityID), recs[i].Data)
			if oerr != nil {
				err = oerr
				break
			}
			recs[i].Data = opened
		}
	}
	httpx.WritePage(w, r, "events", recs, err)
}

type featureRequest struct {
	Name        string `json:"name"`
	EntityType  string `json:"entity_type"`
	EventName   string `json:"event_name"`
	Aggregation string `json:"aggregation"`
	Field       string `json:"field,omitempty"`
	WindowHours int    `json:"window_hours"`
}

func (s *Service) defineFeature(w http.ResponseWriter, r *http.Request) {
	var req featureRequest
	httpx.Emit(w, r, &req, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.DefineFeature(r.Context(), id, domain.DefineFeature{
			Name: req.Name, EntityType: req.EntityType, EventName: req.EventName,
			Aggregation: domain.Aggregation(req.Aggregation), Field: req.Field, WindowHours: req.WindowHours,
		})
	})
}

func (s *Service) listFeatures(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	recs, err := features.List(r.Context(), s.store, id, r.URL.Query().Get("type"))
	httpx.WriteList(w, "features", recs, err)
}

func (s *Service) computeFeatures(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	entityType, entityID := r.PathValue("type"), r.PathValue("id")
	// ?as_of=<RFC3339> reproduces the POINT-IN-TIME values as of a past instant (fresh
	// fold, no cache); without it a live read is served through the materialized cache.
	if raw := strings.TrimSpace(r.URL.Query().Get("as_of")); raw != "" {
		asOf, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, fmt.Errorf("as_of must be RFC3339: %w", err))
			return
		}
		knowledgeAt := asOf.UTC()
		if knowledgeRaw := strings.TrimSpace(r.URL.Query().Get("knowledge_at")); knowledgeRaw != "" {
			knowledgeAt, err = time.Parse(time.RFC3339, knowledgeRaw)
			if err != nil {
				httpx.Error(w, http.StatusBadRequest, fmt.Errorf("knowledge_at must be RFC3339: %w", err))
				return
			}
			knowledgeAt = knowledgeAt.UTC()
		}
		vals, err := features.ComputeAt(
			r.Context(), s.store, id, entityType, entityID, asOf.UTC(), knowledgeAt,
		)
		httpx.WriteList(w, "features", vals, err)
		return
	}
	vals, err := features.ComputeCached(r.Context(), s.store, id, entityType, entityID, s.now())
	httpx.WriteList(w, "features", vals, err)
}

type connectorRequest struct {
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config,omitempty"`
}

func (s *Service) defineConnector(w http.ResponseWriter, r *http.Request) {
	var req connectorRequest
	httpx.Emit(w, r, &req, func(id identity.Identity) (eventlog.Envelope, error) {
		// Validate the type-specific config up front (known types only — an unknown
		// type falls through to the command's domain validation for its canonical
		// error). Runs on the plaintext config, before secrets are sealed.
		if domain.ValidConnectorType(req.Type) {
			if err := connectors.ValidateConfig(req.Type, req.Config); err != nil {
				return eventlog.Envelope{}, err
			}
		}
		// Seal credential fields under the keyring, or redact them when no keyring is
		// configured — so plaintext credentials never reach the event log or the
		// tenant-readable audit surface regardless of sealing config.
		cfg, err := connectors.SealConfigForRecord(req.Config, s.secrets, connectors.SecretLocation{
			Org: id.Org, Workspace: id.Workspace, Connector: req.Name,
		})
		if err != nil {
			return eventlog.Envelope{}, err
		}
		return s.cmd.DefineConnector(r.Context(), id, domain.DefineConnector{Name: req.Name, Type: domain.ConnectorType(req.Type), Config: cfg})
	})
}

func (s *Service) connectorCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := httpx.Caller(w, r); !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"templates": connectors.Catalog()})
}

func (s *Service) listConnectors(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	recs, err := connectors.List(r.Context(), s.store, id, r.URL.Query().Get("type"))
	// Mask credential config fields at the HTTP boundary — secrets never leave the
	// server (the fetch path reads the real config via connectors.Read).
	for i := range recs {
		recs[i] = recs[i].Redacted()
	}
	httpx.WriteList(w, "connectors", recs, err)
}

// fetchConnector invokes a defined connector (the external effect) and records
// the result as an event, so the response is auditable and replay-stable.
func (s *Service) fetchConnector(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req struct {
		Params json.RawMessage `json:"params,omitempty"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	name := r.PathValue("name")
	resp, err := connectors.InvokeWithSecrets(r.Context(), s.store, id, name, req.Params, s.egress, s.secrets)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err)
		return
	}
	fetchID, event, err := s.cmd.RecordFetch(r.Context(), id, name, req.Params, resp)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"fetch_id": fetchID, "response": resp, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) listFetches(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	recs, err := connectors.ListFetches(r.Context(), s.store, id, r.PathValue("name"))
	httpx.WriteList(w, "fetches", recs, err)
}
