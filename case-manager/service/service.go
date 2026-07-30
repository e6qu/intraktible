// SPDX-License-Identifier: AGPL-3.0-or-later

// Package service is the Case Manager's HTTP surface (imperative shell): the case
// queue, detail, and lifecycle endpoints.
package service

import (
	"context"
	"encoding/json"
	"fmt"
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
	cmd        *command.Handler
	store      store.Store
	now        func() time.Time
	governance Governance
}

// Governance resolves mutable subject privacy state without coupling the Case
// Manager to the platform erasure or statutory-retention implementations.
type Governance interface {
	Status(context.Context, identity.Identity, string, time.Time) (cases.SubjectGovernance, error)
}

// New builds the service.
func New(cmd *command.Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the clock the SLA math reads (deterministic tests, the
// demo seeder) and returns the service.
func (s *Service) WithNow(now func() time.Time) *Service {
	s.now = now
	return s
}

// WithGovernance wires authoritative retention, hold, and erasure state.
func (s *Service) WithGovernance(governance Governance) *Service {
	s.governance = governance
	return s
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
	mux.HandleFunc("POST /v1/cases/sla-sweep", s.slaSweep)
	s.enterpriseRoutes(mux)
}

// slaSweep emits SLA-breach events for the tenant's overdue open cases (the push
// side of SLA tracking — a scheduler/cron calls it). It returns the breached ids.
func (s *Service) slaSweep(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	breached, seq, err := s.cmd.SweepSLAWithSeq(r.Context(), id, s.now())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"breached": breached, "count": len(breached), "seq": seq,
	})
}

type reviewRequest struct {
	CompanyName      string          `json:"company_name"`
	CaseType         string          `json:"case_type"`
	Priority         string          `json:"priority,omitempty"`
	Jurisdiction     string          `json:"jurisdiction,omitempty"`
	Subject          string          `json:"subject,omitempty"`
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
		Priority:         domain.Priority(req.Priority),
		Jurisdiction:     req.Jurisdiction,
		Subject:          req.Subject,
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
	recs, err := cases.List(r.Context(), s.store, id, s.filterFrom(r))
	now := s.now()
	for i := range recs {
		cases.AnnotateSLA(&recs[i], now)
		if err := s.annotateRead(r.Context(), id, string(httpx.RoleOf(r.Context())), &recs[i]); err != nil {
			httpx.Error(w, http.StatusInternalServerError, err)
			return
		}
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
	recs, err := cases.List(r.Context(), s.store, id, s.filterFrom(r))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, cases.Summarize(recs, s.now()))
}

func (s *Service) filterFrom(r *http.Request) cases.Filter {
	q := r.URL.Query()
	return cases.Filter{
		Status:   q.Get("status"),
		CaseType: q.Get("type"),
		Assignee: q.Get("assignee"),
		Queue:    q.Get("queue"), Priority: q.Get("priority"),
		Jurisdiction: q.Get("jurisdiction"), Subject: q.Get("subject"),
		Query: q.Get("q"), SLAState: q.Get("sla_state"),
		Now: s.now(),
	}
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	c, found, err := cases.Read(r.Context(), s.store, id, r.PathValue("case_id"))
	if found {
		cases.AnnotateSLA(&c, s.now())
		if err := s.annotateRead(r.Context(), id, string(httpx.RoleOf(r.Context())), &c); err != nil {
			httpx.Error(w, http.StatusInternalServerError, err)
			return
		}
	}
	httpx.WriteOne(w, c, found, err, "case not found")
}

func (s *Service) annotateRead(
	ctx context.Context,
	id identity.Identity,
	role string,
	view *cases.CaseView,
) error {
	if view.CaseTypeVersion > 0 {
		published, found, err := cases.CaseTypeVersion(ctx, s.store, id, view.CaseType, view.CaseTypeVersion)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("case-manager: missing pinned case type %q version %d", view.CaseType, view.CaseTypeVersion)
		}
		masked, err := cases.MaskPII(*view, published.Definition, role)
		if err != nil {
			return err
		}
		*view = masked
	}
	if s.governance != nil && view.Subject != "" {
		status, err := s.governance.Status(ctx, id, view.Subject, s.now())
		if err != nil {
			return err
		}
		view.SubjectGovernance = &status
		if status.Erased {
			view.Context = json.RawMessage(`{"erased":"[crypto-shredded]"}`)
		}
	}
	for i := range view.Attachments {
		if s.governance != nil && view.Attachments[i].Subject != "" {
			attachmentStatus, err := s.governance.Status(ctx, id, view.Attachments[i].Subject, s.now())
			if err != nil {
				return err
			}
			view.Attachments[i].LegalHold = attachmentStatus.LegalHold
			if attachmentStatus.Retained {
				view.Attachments[i].RetainUntil = attachmentStatus.RetainUntil.Format(time.RFC3339)
			}
			view.Attachments[i].Erased = attachmentStatus.Erased
		}
		// A storage pointer is a capability, not list/detail metadata. Every
		// ordinary read redacts it; the dedicated access command records the
		// purpose, actor, and time before returning it.
		view.Attachments[i].StorageRef = ""
	}
	return nil
}

func (s *Service) assign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Assignee string `json:"assignee"`
		// Reassign takes a case away from its current assignee. Assigning an already
		// owned case without it is refused, so one claim cannot silently overwrite another.
		Reassign bool `json:"reassign,omitempty"`
	}
	httpx.Emit(w, r, &req, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.AssignCase(r.Context(), id, domain.AssignCase{
			CaseID: r.PathValue("case_id"), Assignee: req.Assignee, Reassign: req.Reassign,
		})
	})
}

func (s *Service) status(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status string `json:"status"`
	}
	httpx.Emit(w, r, &req, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.SetStatus(r.Context(), id, domain.SetStatus{
			CaseID: r.PathValue("case_id"), Status: domain.CaseStatus(req.Status),
			Role: string(httpx.RoleOf(r.Context())),
		})
	})
}

func (s *Service) note(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	httpx.Emit(w, r, &req, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.AddNote(r.Context(), id, domain.AddNote{CaseID: r.PathValue("case_id"), Text: req.Text})
	})
}
