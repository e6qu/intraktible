// SPDX-License-Identifier: AGPL-3.0-or-later

// Package service is the Decision Engine's HTTP surface (imperative shell): flow
// management endpoints wiring the command write side and the flows read model.
package service

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Service wires flow commands, the decide runtime, and the read models to HTTP.
type Service struct {
	cmd    *command.Handler
	decide *command.DecideHandler
	store  store.Store
}

// New builds the service.
func New(cmd *command.Handler, decide *command.DecideHandler, st store.Store) *Service {
	return &Service{cmd: cmd, decide: decide, store: st}
}

// caller resolves the authenticated identity, writing a 401 and returning
// ok=false when absent.
func (s *Service) caller(w http.ResponseWriter, r *http.Request) (identity.Identity, bool) {
	id, ok := identity.From(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, errors.New("authentication required"))
	}
	return id, ok
}

// writeList responds with a read-model listing under the given JSON key, mapping
// a store error to 500.
func writeList[T any](w http.ResponseWriter, key string, items []T, err error) {
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{key: items})
}

// writeOne responds with a single read-model document, mapping a store error to
// 500 and a missing document to 404 with notFound.
func writeOne[T any](w http.ResponseWriter, item T, found bool, err error, notFound string) {
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, errors.New(notFound))
		return
	}
	httpx.JSON(w, http.StatusOK, item)
}

// Routes registers the flow-management, decide, and decision-history endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/flows", s.create)
	mux.HandleFunc("GET /v1/flows", s.list)
	mux.HandleFunc("GET /v1/flows/{flow_id}", s.get)
	mux.HandleFunc("POST /v1/flows/{flow_id}/versions", s.publish)
	mux.HandleFunc("POST /v1/flows/{slug}/{env}/decide", s.runDecide)
	mux.HandleFunc("GET /v1/decisions", s.listDecisions)
	mux.HandleFunc("GET /v1/decisions/{decision_id}", s.getDecision)
}

type createRequest struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	id, ok := s.caller(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	flowID, e, err := s.cmd.CreateFlow(r.Context(), id, domain.CreateFlow{Slug: req.Slug, Name: req.Name})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"flow_id": flowID, "event_id": e.ID, "seq": e.Seq})
}

type publishRequest struct {
	Graph       events.Graph    `json:"graph"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

func (s *Service) publish(w http.ResponseWriter, r *http.Request) {
	id, ok := s.caller(w, r)
	if !ok {
		return
	}
	var req publishRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	version, etag, e, err := s.cmd.PublishVersion(r.Context(), id, domain.PublishVersion{
		FlowID:      r.PathValue("flow_id"),
		Graph:       req.Graph,
		InputSchema: req.InputSchema,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"version": version, "etag": etag, "event_id": e.ID, "seq": e.Seq,
	})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := s.caller(w, r)
	if !ok {
		return
	}
	fvs, err := flows.List(r.Context(), s.store, id)
	writeList(w, "flows", fvs, err)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := s.caller(w, r)
	if !ok {
		return
	}
	fv, found, err := flows.Read(r.Context(), s.store, id, r.PathValue("flow_id"))
	writeOne(w, fv, found, err, "flow not found")
}

type decideRequest struct {
	Data     map[string]any  `json:"data"`
	MockData map[string]any  `json:"mock_data,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	Control  json.RawMessage `json:"control,omitempty"`
}

type decideResponse struct {
	DecisionID string         `json:"decision_id"`
	Status     string         `json:"status"`
	Data       map[string]any `json:"data,omitempty"`
	Error      string         `json:"error,omitempty"`
}

// runDecide executes a published flow. A flow whose logic errors is a recorded
// "failed" decision returned with HTTP 200 and status "failed" (the call
// succeeded; the decision outcome did not); only lookup/validation problems 4xx.
func (s *Service) runDecide(w http.ResponseWriter, r *http.Request) {
	id, ok := s.caller(w, r)
	if !ok {
		return
	}
	var req decideRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.decide.Decide(r.Context(), id, r.PathValue("slug"), r.PathValue("env"), req.Data)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, decideResponse{
		DecisionID: result.DecisionID, Status: result.Status, Data: result.Output, Error: result.Error,
	})
}

func (s *Service) listDecisions(w http.ResponseWriter, r *http.Request) {
	id, ok := s.caller(w, r)
	if !ok {
		return
	}
	recs, err := history.List(r.Context(), s.store, id)
	writeList(w, "decisions", recs, err)
}

func (s *Service) getDecision(w http.ResponseWriter, r *http.Request) {
	id, ok := s.caller(w, r)
	if !ok {
		return
	}
	rec, found, err := history.Read(r.Context(), s.store, id, r.PathValue("decision_id"))
	writeOne(w, rec, found, err, "decision not found")
}
