// SPDX-License-Identifier: AGPL-3.0-or-later

package grants

import (
	"net/http"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Service is the per-flow grants HTTP surface (admin-gated by requiredRole).
type Service struct {
	cmd   *Handler
	store store.Store
}

// New wires the grant write side and read model to HTTP.
func New(cmd *Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the per-flow grant endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/flows/{flow_id}/grants", s.list)
	mux.HandleFunc("POST /v1/flows/{flow_id}/grants", s.add)
	mux.HandleFunc("DELETE /v1/flows/{flow_id}/grants/{grant_id}", s.revoke)
}

type addRequest struct {
	Actor       string `json:"actor"`
	Environment string `json:"environment"`
}

func (s *Service) add(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req addRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	env := req.Environment
	if env == "" {
		env = "*"
	}
	gid, e, err := s.cmd.Add(r.Context(), id, r.PathValue("flow_id"), req.Actor, env)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"grant_id": gid, "event_id": e.ID, "seq": e.Seq})
}

func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	httpx.Act(w, r, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.Revoke(r.Context(), id, r.PathValue("flow_id"), r.PathValue("grant_id"))
	})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	vs, err := ForFlow(r.Context(), s.store, id, r.PathValue("flow_id"))
	httpx.WriteList(w, "grants", vs, err)
}
