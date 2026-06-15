// SPDX-License-Identifier: AGPL-3.0-or-later

// Package service is the Case Manager's HTTP surface (imperative shell): the case
// queue, detail, and lifecycle endpoints.
package service

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/command"
	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Service wires the case commands and the case read model to HTTP.
type Service struct {
	cmd   *command.Handler
	store store.Store
}

// New builds the service.
func New(cmd *command.Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the case-management endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/cases", s.requestReview)
	mux.HandleFunc("GET /v1/cases", s.list)
	mux.HandleFunc("GET /v1/cases/summary", s.summary)
	mux.HandleFunc("GET /v1/cases/{case_id}", s.get)
	mux.HandleFunc("POST /v1/cases/{case_id}/assign", s.assign)
	mux.HandleFunc("POST /v1/cases/{case_id}/status", s.status)
	mux.HandleFunc("POST /v1/cases/{case_id}/notes", s.note)
}

type reviewRequest struct {
	CompanyName      string          `json:"company_name"`
	CaseType         string          `json:"case_type"`
	SLADays          int             `json:"sla_days"`
	Context          json.RawMessage `json:"context,omitempty"`
	SourceDecisionID string          `json:"source_decision_id,omitempty"`
}

func (s *Service) requestReview(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req reviewRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	caseID, e, err := s.cmd.RequestReview(r.Context(), id, domain.RequestReview{
		CompanyName:      req.CompanyName,
		CaseType:         req.CaseType,
		SLADays:          req.SLADays,
		Context:          req.Context,
		SourceDecisionID: req.SourceDecisionID,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"case_id": caseID, "event_id": e.ID, "seq": e.Seq})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	recs, err := cases.List(r.Context(), s.store, id, filterFrom(r))
	now := time.Now().UTC()
	for i := range recs {
		cases.AnnotateSLA(&recs[i], now)
	}
	httpx.WriteList(w, "cases", recs, err)
}

// summary returns the queue roll-up (counts by status, unassigned, SLA buckets)
// over the same filtered set as the list endpoint.
func (s *Service) summary(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	recs, err := cases.List(r.Context(), s.store, id, filterFrom(r))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, cases.Summarize(recs, time.Now().UTC()))
}

func filterFrom(r *http.Request) cases.Filter {
	q := r.URL.Query()
	return cases.Filter{
		Status:   q.Get("status"),
		CaseType: q.Get("type"),
		Assignee: q.Get("assignee"),
	}
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	c, found, err := cases.Read(r.Context(), s.store, id, r.PathValue("case_id"))
	if found {
		cases.AnnotateSLA(&c, time.Now().UTC())
	}
	httpx.WriteOne(w, c, found, err, "case not found")
}

// mutate is the shared shape of the case-mutating endpoints: authenticate,
// decode body into req, run the command, and respond 202. run is called after
// req is decoded, so it can read the decoded fields.
func (s *Service) mutate(w http.ResponseWriter, r *http.Request, req any, run func(identity.Identity) (eventlog.Envelope, error)) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	if err := httpx.DecodeJSON(r, req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	e, err := run(id)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"event_id": e.ID, "seq": e.Seq})
}

func (s *Service) assign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Assignee string `json:"assignee"`
	}
	s.mutate(w, r, &req, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.AssignCase(r.Context(), id, domain.AssignCase{CaseID: r.PathValue("case_id"), Assignee: req.Assignee})
	})
}

func (s *Service) status(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status string `json:"status"`
	}
	s.mutate(w, r, &req, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.SetStatus(r.Context(), id, domain.SetStatus{CaseID: r.PathValue("case_id"), Status: req.Status})
	})
}

func (s *Service) note(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	s.mutate(w, r, &req, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.AddNote(r.Context(), id, domain.AddNote{CaseID: r.PathValue("case_id"), Text: req.Text})
	})
}
