// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications

import (
	"net/http"

	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// Service is the inbox HTTP surface (imperative shell).
type Service struct {
	cmd   *Handler
	store store.Store
}

// New wires the inbox write side and read model to HTTP.
func New(cmd *Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the notification endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/notifications", s.list)
	mux.HandleFunc("POST /v1/notifications/{notification_id}/read", s.markRead)
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	vs, err := List(r.Context(), s.store, id)
	httpx.WriteList(w, "notifications", vs, err)
}

func (s *Service) markRead(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	e, err := s.cmd.MarkRead(r.Context(), id, r.PathValue("notification_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"event_id": e.ID, "seq": e.Seq})
}
