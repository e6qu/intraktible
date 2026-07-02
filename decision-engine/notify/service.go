// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"net/http"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Service is the webhook-subscription HTTP surface (imperative shell).
type Service struct {
	cmd   *Handler
	store store.Store
}

// New wires the webhook write side and read model to HTTP.
func New(cmd *Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the webhook subscription endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/webhooks", s.subscribe)
	mux.HandleFunc("GET /v1/webhooks", s.list)
	mux.HandleFunc("DELETE /v1/webhooks/{webhook_id}", s.unsubscribe)
}

type subscribeRequest struct {
	URL      string   `json:"url"`
	Note     string   `json:"note,omitempty"`
	Template string   `json:"template,omitempty"`
	Events   []string `json:"events,omitempty"`
}

func (s *Service) subscribe(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req subscribeRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	wid, e, err := s.cmd.Subscribe(r.Context(), id, req.URL, req.Note, req.Template, req.Events)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"webhook_id": wid, "event_id": e.ID, "seq": e.Seq})
}

func (s *Service) unsubscribe(w http.ResponseWriter, r *http.Request) {
	httpx.Act(w, r, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.Unsubscribe(r.Context(), id, r.PathValue("webhook_id"))
	})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	vs, err := List(r.Context(), s.store, id)
	httpx.WriteList(w, "webhooks", vs, err)
}
