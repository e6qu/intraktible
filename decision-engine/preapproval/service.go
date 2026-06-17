// SPDX-License-Identifier: AGPL-3.0-or-later

package preapproval

import (
	"encoding/json"
	"net/http"

	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// Service is the pre-approval HTTP surface (imperative shell).
type Service struct {
	cmd   *Handler
	store store.Store
}

// New wires the pre-approval write side and read model to HTTP.
func New(cmd *Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the pre-approval endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/preapprovals", s.grant)
	mux.HandleFunc("GET /v1/preapprovals", s.list)
	mux.HandleFunc("GET /v1/preapprovals/{type}/{id}", s.get)
	mux.HandleFunc("POST /v1/preapprovals/{type}/{id}/revoke", s.revoke)
}

type grantRequest struct {
	EntityType    string          `json:"entity_type"`
	EntityID      string          `json:"entity_id"`
	Disposition   string          `json:"disposition,omitempty"`
	Terms         json.RawMessage `json:"terms,omitempty"`
	PolicyID      string          `json:"policy_id,omitempty"`
	PolicyVersion int             `json:"policy_version,omitempty"`
	FlowSlug      string          `json:"flow_slug,omitempty"`
	ValidDays     int             `json:"valid_days"`
	Note          string          `json:"note,omitempty"`
}

func (s *Service) grant(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req grantRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	paID, e, err := s.cmd.Grant(r.Context(), id, GrantCmd(req))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"preapproval_id": paID, "event_id": e.ID, "seq": e.Seq})
}

type revokeRequest struct {
	Reason string `json:"reason,omitempty"`
}

func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req revokeRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	e, err := s.cmd.Revoke(r.Context(), id, r.PathValue("type"), r.PathValue("id"), req.Reason)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"event_id": e.ID, "seq": e.Seq})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	vs, err := List(r.Context(), s.store, id)
	httpx.WriteList(w, "preapprovals", vs, err)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	v, found, err := Read(r.Context(), s.store, id, r.PathValue("type"), r.PathValue("id"))
	httpx.WriteOne(w, v, found, err, "pre-approval not found")
}
