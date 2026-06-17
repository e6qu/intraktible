// SPDX-License-Identifier: AGPL-3.0-or-later

package privacy

import (
	"net/http"

	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// Service is the masking-config HTTP surface (imperative shell).
type Service struct {
	cmd   *Handler
	store store.Store
}

// New wires the masking-config write side and read model to HTTP.
func New(cmd *Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the masking-config endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/privacy", s.get)
	mux.HandleFunc("PUT /v1/privacy", s.set)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	v, found, err := Read(r.Context(), s.store, id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		v = View{Org: id.Org, Workspace: id.Workspace, Fields: []string{}}
	}
	httpx.JSON(w, http.StatusOK, v)
}

type setRequest struct {
	Fields []string `json:"fields"`
}

func (s *Service) set(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req setRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	e, err := s.cmd.SetFields(r.Context(), id, req.Fields)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"event_id": e.ID, "seq": e.Seq})
}
