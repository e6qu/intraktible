// SPDX-License-Identifier: AGPL-3.0-or-later

package comments

import (
	"net/http"

	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// Service is the comments HTTP surface (imperative shell): read or append to a
// subject's thread. Any subject can carry a thread — deployment requests,
// decisions, cases — so workflow surfaces share one commenting capability.
type Service struct {
	cmd   *Handler
	store store.Store
}

// New wires the comments write side and read model to HTTP.
func New(cmd *Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the comment endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/comments/{subject_type}/{subject_id}", s.list)
	mux.HandleFunc("POST /v1/comments/{subject_type}/{subject_id}", s.post)
	mux.HandleFunc(
		"POST /v1/comments/{subject_type}/{subject_id}/{comment_id}/resolve",
		s.resolve,
	)
	mux.HandleFunc(
		"POST /v1/comments/{subject_type}/{subject_id}/{comment_id}/reopen",
		s.reopen,
	)
}

func (s *Service) resolve(w http.ResponseWriter, r *http.Request) {
	s.setResolved(w, r, true)
}

func (s *Service) reopen(w http.ResponseWriter, r *http.Request) {
	s.setResolved(w, r, false)
}

func (s *Service) setResolved(w http.ResponseWriter, r *http.Request, resolved bool) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	event, err := s.cmd.SetResolved(
		r.Context(), id,
		Subject{Type: r.PathValue("subject_type"), ID: r.PathValue("subject_id")},
		r.PathValue("comment_id"), resolved,
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"event_id": event.ID, "seq": event.Seq, "resolved": resolved,
	})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	vs, err := List(r.Context(), s.store, id, r.PathValue("subject_type"), r.PathValue("subject_id"))
	httpx.WriteList(w, "comments", vs, err)
}

type postRequest struct {
	Body     string `json:"body"`
	ParentID string `json:"parent_id,omitempty"`
}

func (s *Service) post(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req postRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	cid, e, err := s.cmd.Post(r.Context(), id, Subject{Type: r.PathValue("subject_type"), ID: r.PathValue("subject_id")}, req.Body, req.ParentID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"comment_id": cid, "event_id": e.ID, "seq": e.Seq})
}
