// SPDX-License-Identifier: AGPL-3.0-or-later

// Package service is the hello feature's HTTP surface (imperative shell).
package service

import (
	"errors"
	"net/http"

	"github.com/e6qu/intraktible/hello/command"
	"github.com/e6qu/intraktible/hello/domain"
	"github.com/e6qu/intraktible/hello/stats"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Service wires the hello command + read model to HTTP.
type Service struct {
	cmd   *command.Handler
	store store.Store
}

// New builds the service.
func New(cmd *command.Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the hello endpoints on mux.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/hello", s.say)
	mux.HandleFunc("GET /v1/hello/stats", s.stats)
}

type sayRequest struct {
	Name string `json:"name"`
}

func (s *Service) say(w http.ResponseWriter, r *http.Request) {
	id, ok := identity.From(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, errors.New("authentication required"))
		return
	}
	var req sayRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	e, err := s.cmd.SayHello(r.Context(), id, domain.SayHello{Name: req.Name})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"event_id": e.ID, "seq": e.Seq})
}

func (s *Service) stats(w http.ResponseWriter, r *http.Request) {
	id, ok := identity.From(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, errors.New("authentication required"))
		return
	}
	st, err := stats.Read(r.Context(), s.store, id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, st)
}
