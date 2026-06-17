// SPDX-License-Identifier: AGPL-3.0-or-later

// Package service is the Context Layer's HTTP surface (imperative shell): record
// custom entities and events, and read the entity store + per-entity event log.
package service

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/context-layer/command"
	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/context-layer/features"
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
	secrets connectors.SecretBox
}

// Option configures a Service.
type Option func(*Service)

// WithEgress sets the HTTP connector's egress policy (SSRF guard). The default
// (zero value) blocks loopback/private targets.
func WithEgress(p connectors.EgressPolicy) Option {
	return func(s *Service) { s.egress = p }
}

// WithSecrets enables connector credential encryption/decryption at the HTTP
// boundary. Credential fields are encrypted before ConnectorDefined is emitted.
func WithSecrets(box connectors.SecretBox) Option {
	return func(s *Service) { s.secrets = box }
}

// New builds the service.
func New(cmd *command.Handler, st store.Store, opts ...Option) *Service {
	s := &Service{cmd: cmd, store: st}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Routes registers the Context Layer endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/context/entities", s.recordEntity)
	mux.HandleFunc("GET /v1/context/entities", s.listEntities)
	mux.HandleFunc("GET /v1/context/entities/{type}/{id}", s.getEntity)
	mux.HandleFunc("GET /v1/context/entities/{type}/{id}/events", s.listEvents)
	mux.HandleFunc("GET /v1/context/entities/{type}/{id}/features", s.computeFeatures)
	mux.HandleFunc("POST /v1/context/events", s.recordEvent)
	mux.HandleFunc("POST /v1/context/features", s.defineFeature)
	mux.HandleFunc("GET /v1/context/features", s.listFeatures)
	mux.HandleFunc("POST /v1/context/connectors", s.defineConnector)
	mux.HandleFunc("GET /v1/context/connectors", s.listConnectors)
	mux.HandleFunc("GET /v1/context/connectors/catalog", s.connectorCatalog)
	mux.HandleFunc("POST /v1/context/connectors/{name}/fetch", s.fetchConnector)
	mux.HandleFunc("GET /v1/context/connectors/{name}/fetches", s.listFetches)
}

type entityRequest struct {
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	Attributes json.RawMessage `json:"attributes,omitempty"`
}

func (s *Service) recordEntity(w http.ResponseWriter, r *http.Request) {
	var req entityRequest
	httpx.Emit(w, r, &req, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.RecordEntity(r.Context(), id, domain.RecordEntity{
			EntityType: req.EntityType, EntityID: req.EntityID, Attributes: req.Attributes,
		})
	})
}

type eventRequest struct {
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	EventName  string          `json:"event_name"`
	Data       json.RawMessage `json:"data,omitempty"`
	OccurredAt time.Time       `json:"occurred_at,omitempty"`
}

func (s *Service) recordEvent(w http.ResponseWriter, r *http.Request) {
	var req eventRequest
	httpx.Emit(w, r, &req, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.RecordEvent(r.Context(), id, domain.RecordEvent{
			EntityType: req.EntityType, EntityID: req.EntityID, EventName: req.EventName,
			Data: req.Data, OccurredAt: req.OccurredAt,
		})
	})
}

func (s *Service) listEntities(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	recs, err := entities.ListEntities(r.Context(), s.store, id, r.URL.Query().Get("type"))
	httpx.WriteList(w, "entities", recs, err)
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
	httpx.WriteList(w, "events", recs, err)
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
			Aggregation: req.Aggregation, Field: req.Field, WindowHours: req.WindowHours,
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
	vals, err := features.Compute(r.Context(), s.store, id, r.PathValue("type"), r.PathValue("id"), time.Now().UTC())
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
		cfg, err := connectors.EncryptSecrets(req.Config, s.secrets)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		return s.cmd.DefineConnector(r.Context(), id, domain.DefineConnector{Name: req.Name, Type: req.Type, Config: cfg})
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
	fetchID, _, err := s.cmd.RecordFetch(r.Context(), id, name, req.Params, resp)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"fetch_id": fetchID, "response": resp})
}

func (s *Service) listFetches(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	recs, err := connectors.ListFetches(r.Context(), s.store, id, r.PathValue("name"))
	httpx.WriteList(w, "fetches", recs, err)
}
