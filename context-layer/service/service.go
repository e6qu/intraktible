// SPDX-License-Identifier: AGPL-3.0-or-later

// Package service is the Context Layer's HTTP surface (imperative shell): record
// custom entities and events, and read the entity store + per-entity event log.
package service

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/context-layer/command"
	"github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// Service wires the Context Layer commands and read model to HTTP.
type Service struct {
	cmd   *command.Handler
	store store.Store
}

// New builds the service.
func New(cmd *command.Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the Context Layer endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/context/entities", s.recordEntity)
	mux.HandleFunc("GET /v1/context/entities", s.listEntities)
	mux.HandleFunc("GET /v1/context/entities/{type}/{id}", s.getEntity)
	mux.HandleFunc("GET /v1/context/entities/{type}/{id}/events", s.listEvents)
	mux.HandleFunc("POST /v1/context/events", s.recordEvent)
}

type entityRequest struct {
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	Attributes json.RawMessage `json:"attributes,omitempty"`
}

func (s *Service) recordEntity(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req entityRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	e, err := s.cmd.RecordEntity(r.Context(), id, domain.RecordEntity{
		EntityType: req.EntityType,
		EntityID:   req.EntityID,
		Attributes: req.Attributes,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"event_id": e.ID, "seq": e.Seq})
}

type eventRequest struct {
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	EventName  string          `json:"event_name"`
	Data       json.RawMessage `json:"data,omitempty"`
	OccurredAt time.Time       `json:"occurred_at,omitempty"`
}

func (s *Service) recordEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req eventRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	e, err := s.cmd.RecordEvent(r.Context(), id, domain.RecordEvent{
		EntityType: req.EntityType,
		EntityID:   req.EntityID,
		EventName:  req.EventName,
		Data:       req.Data,
		OccurredAt: req.OccurredAt,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"event_id": e.ID, "seq": e.Seq})
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
