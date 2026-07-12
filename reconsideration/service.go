// SPDX-License-Identifier: AGPL-3.0-or-later

package reconsideration

import (
	"fmt"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// Service exposes the reconsideration surface: record and read the human review of an
// automated adverse decision. Reads are viewer-level; the write is operator-level
// (enforced by the route policy).
type Service struct {
	cmd   *Handler
	store store.Store
	now   func() time.Time
}

// New wires the reconsideration write side and read model to HTTP.
func New(cmd *Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st, now: func() time.Time { return time.Now().UTC() }}
}

// Routes registers the reconsideration endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/decisions/{decision_id}/reconsideration", s.get)
	mux.HandleFunc("POST /v1/decisions/{decision_id}/reconsideration", s.record)
	mux.HandleFunc("GET /v1/reconsiderations", s.list)
}

// list returns every recorded human review for the tenant — the audit trail of which
// automated declines a person reviewed, and what they concluded.
func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	reviews, err := List(r.Context(), s.store, id)
	httpx.WriteList(w, "reconsiderations", reviews, err)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	rec, found, err := Read(r.Context(), s.store, id, r.PathValue("decision_id"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.JSON(w, http.StatusOK, map[string]any{"decision_id": r.PathValue("decision_id"), "reviewed": false})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"reviewed": true, "review": rec})
}

type recordRequest struct {
	Basis     Basis   `json:"basis"`
	Outcome   Outcome `json:"outcome"`
	Rationale string  `json:"rationale"`
}

// record logs a human review of an automated decline. It refuses (400) unless the
// decision is a completed, solely-automated decline: only such a decision carries the
// Art. 22 right to human intervention. A decision that already had a person in the
// loop (routed to manual_review, or resumed by a reviewer) is not eligible — that
// human involvement is the safeguard, and re-recording it here would be misleading.
func (s *Service) record(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	decisionID := r.PathValue("decision_id")
	var req recordRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	rec, found, err := history.Read(r.Context(), s.store, id, decisionID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("decision %s not found", decisionID))
		return
	}
	if err := eligible(rec); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	e, err := s.cmd.Record(r.Context(), id, RecordCmd{
		DecisionID: decisionID, Subject: subjectOf(rec),
		Basis: req.Basis, Outcome: req.Outcome, Rationale: req.Rationale,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"event_id": e.ID, "seq": e.Seq})
}

// eligible reports why a decision may not be reconsidered, or nil when it may: it must
// be a completed decline that ran solely automated (no case, no human resume).
func eligible(rec history.Record) error {
	if rec.Status != "completed" {
		return fmt.Errorf("decision %s is %s, not a completed decision", rec.DecisionID, rec.Status)
	}
	if policy.Disposition(rec.Disposition) != policy.Decline {
		return fmt.Errorf("decision %s was not declined (disposition %q); reconsideration applies to an adverse outcome", rec.DecisionID, rec.Disposition)
	}
	if rec.CaseID != "" || rec.HumanReviewed {
		return fmt.Errorf("decision %s already had human review; the Art. 22 reconsideration record is for a solely-automated decision", rec.DecisionID)
	}
	return nil
}

// subjectOf keys a decision's data subject as "type/id", or "" when it referenced no
// entity — the same key consent, PII sealing, and erasure use.
func subjectOf(rec history.Record) string {
	if rec.EntityType == "" || rec.EntityID == "" {
		return ""
	}
	return rec.EntityType + "/" + rec.EntityID
}
