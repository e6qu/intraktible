// SPDX-License-Identifier: AGPL-3.0-or-later

package jurisdiction

import (
	"net/http"

	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// Service is the jurisdiction-setting HTTP surface. Reads are viewer-level; the write
// is a compliance control (admin), enforced by the default route policy.
type Service struct {
	cmd   *Handler
	store store.Store
}

// New wires the jurisdiction write side and read model to HTTP.
func New(cmd *Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the jurisdiction endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/compliance/jurisdiction", s.get)
	mux.HandleFunc("PUT /v1/compliance/jurisdiction", s.set)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	regimes, err := Applicable(r.Context(), s.store, id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	v, configured, err := Read(r.Context(), s.store, id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"regimes": regimes, "configured": configured, "updated_at": v.UpdatedAt, "updated_by": v.UpdatedBy,
	})
}

type setRequest struct {
	Regimes []string `json:"regimes"`
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
	e, regimes, err := s.cmd.Set(r.Context(), id, req.Regimes)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"event_id": e.ID, "seq": e.Seq, "regimes": regimes})
}
