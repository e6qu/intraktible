// SPDX-License-Identifier: AGPL-3.0-or-later

package erasure

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
)

// RetentionGate reports whether a subject must be retained because a record about them
// is still within its statutory mandatory-retention window — the automatic, rule-driven
// counterpart to a manual legal hold. The composition root supplies the adapter so the
// vault stays free of the compliance-record packages.
type RetentionGate interface {
	Retained(ctx context.Context, id identity.Identity, subject string) (retained bool, reason string, err error)
}

// Service exposes admin-only erasure operations: fulfilling a right-to-erasure
// request (crypto-shred a subject), listing fulfilled erasures, and running a
// retention sweep.
type Service struct {
	vault         *Vault
	retentionGate RetentionGate
}

// NewService builds the erasure HTTP surface.
func NewService(v *Vault) *Service { return &Service{vault: v} }

// WithRetentionGate makes erasure refuse a subject still within a statutory retention
// window (GDPR Art. 17(3)(b)). Without it, only manual legal holds block erasure.
func (s *Service) WithRetentionGate(g RetentionGate) *Service {
	s.retentionGate = g
	return s
}

// Routes registers the erasure endpoints (admin-gated in the middleware).
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/erasure/subjects", s.list)
	mux.HandleFunc("GET /v1/erasure/subjects/{subject}", s.status)
	mux.HandleFunc("POST /v1/erasure/subjects/{subject}", s.erase)
	mux.HandleFunc("POST /v1/erasure/subjects/{subject}/hold", s.hold)
	mux.HandleFunc("POST /v1/erasure/subjects/{subject}/release", s.release)
	mux.HandleFunc("GET /v1/erasure/holds", s.holds)
	mux.HandleFunc("GET /v1/erasure/retention-policy", s.getPolicy)
	mux.HandleFunc("PUT /v1/erasure/retention-policy", s.setPolicy)
	mux.HandleFunc("POST /v1/erasure/retention", s.retention)
}

func (s *Service) getPolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	p, err := s.vault.RetentionPolicyFor(r.Context(), id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

type policyRequest struct {
	RetentionDays int `json:"retention_days"`
}

// setPolicy stores the tenant's retention window (0 disables). The scheduled sweep
// applies it; nothing is erased by this call.
func (s *Service) setPolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req policyRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if err := s.vault.SetRetentionPolicy(r.Context(), id, req.RetentionDays); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"retention_days": req.RetentionDays})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	subjects, err := s.vault.ListErased(r.Context(), id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"erased": subjects})
}

func (s *Service) status(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	subj := r.PathValue("subject")
	erased, err := s.vault.Erased(r.Context(), id, subj)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"subject": subj, "erased": erased})
}

// erase fulfills a right-to-erasure request: it destroys the subject's key, so
// everything sealed under it (in the log and projections) is unrecoverable.
func (s *Service) erase(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	subj := r.PathValue("subject")
	// A statutory retention window is a legal obligation to keep the record (GDPR Art.
	// 17(3)(b)), so it blocks erasure just as a manual hold does — a caller-fixable 409
	// (the record ages out), not a fault.
	if s.retentionGate != nil {
		retained, reason, err := s.retentionGate.Retained(r.Context(), id, subj)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err)
			return
		}
		if retained {
			httpx.Error(w, http.StatusConflict, fmt.Errorf("erasure: %s", reason))
			return
		}
	}
	if err := s.vault.Erase(r.Context(), id, subj); err != nil {
		// A subject under legal hold is a caller-fixable state (release the hold),
		// not a server fault — 409, with the reason, not a 500.
		if errors.Is(err, ErrHeld) {
			httpx.Error(w, http.StatusConflict, err)
			return
		}
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"subject": subj, "erased": true})
}

type holdRequest struct {
	Reason string `json:"reason,omitempty"`
}

// hold places a legal hold on a subject (it survives retention and blocks erasure).
func (s *Service) hold(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req holdRequest
	// The body is optional (a reason is recommended but not required).
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, err)
			return
		}
	}
	subj := r.PathValue("subject")
	if err := s.vault.Hold(r.Context(), id, subj, req.Reason); err != nil {
		httpx.Error(w, statusFor(err), err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"subject": subj, "held": true})
}

// release lifts a subject's legal hold.
func (s *Service) release(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	subj := r.PathValue("subject")
	if err := s.vault.ReleaseHold(r.Context(), id, subj); err != nil {
		httpx.Error(w, statusFor(err), err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"subject": subj, "held": false})
}

func (s *Service) holds(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	held, err := s.vault.ListHeld(r.Context(), id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if held == nil {
		held = []Held{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"held": held})
}

// statusFor maps a hold error to a status: an unknown/not-held subject or an
// already-erased one is a caller error (400/409), anything else a server fault.
func statusFor(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, ErrErased):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

// retention erases every subject older than ?max_age_days, enforcing a retention
// limit (a cron or operator can call it periodically).
func (s *Service) retention(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	days, err := strconv.Atoi(r.URL.Query().Get("max_age_days"))
	if err != nil || days <= 0 {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("max_age_days must be a positive integer"))
		return
	}
	n, err := s.vault.RetentionSweep(r.Context(), id, time.Duration(days)*24*time.Hour)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"erased": n, "max_age_days": days})
}
