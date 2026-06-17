// SPDX-License-Identifier: AGPL-3.0-or-later

package policy

import (
	"net/http"

	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// Service is the policy HTTP surface (imperative shell): author + read policies.
type Service struct {
	cmd   *Handler
	store store.Store
}

// New wires the policy command write side and the policies read model to HTTP.
func New(cmd *Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the policy endpoints on the API mux.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/policies", s.create)
	mux.HandleFunc("GET /v1/policies", s.list)
	mux.HandleFunc("GET /v1/policies/{policy_id}", s.get)
	mux.HandleFunc("POST /v1/policies/{policy_id}/versions", s.publish)
}

type createRequest struct {
	Name     string `json:"name"`
	FlowSlug string `json:"flow_slug"`
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	policyID, e, err := s.cmd.CreatePolicy(r.Context(), id, req.Name, req.FlowSlug)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"policy_id": policyID, "event_id": e.ID, "seq": e.Seq})
}

type publishRequest struct {
	Spec Spec `json:"spec"`
}

func (s *Service) publish(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req publishRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	version, etag, e, err := s.cmd.PublishVersion(r.Context(), id, r.PathValue("policy_id"), req.Spec)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"version": version, "etag": etag, "event_id": e.ID, "seq": e.Seq,
	})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	pvs, err := List(r.Context(), s.store, id)
	httpx.WriteList(w, "policies", pvs, err)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	pv, found, err := Read(r.Context(), s.store, id, r.PathValue("policy_id"))
	httpx.WriteOne(w, pv, found, err, "policy not found")
}
