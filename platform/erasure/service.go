// SPDX-License-Identifier: AGPL-3.0-or-later

package erasure

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/e6qu/intraktible/platform/httpx"
)

// Service exposes admin-only erasure operations: fulfilling a right-to-erasure
// request (crypto-shred a subject), listing fulfilled erasures, and running a
// retention sweep.
type Service struct {
	vault *Vault
}

// NewService builds the erasure HTTP surface.
func NewService(v *Vault) *Service { return &Service{vault: v} }

// Routes registers the erasure endpoints (admin-gated in the middleware).
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/erasure/subjects", s.list)
	mux.HandleFunc("GET /v1/erasure/subjects/{subject}", s.status)
	mux.HandleFunc("POST /v1/erasure/subjects/{subject}", s.erase)
	mux.HandleFunc("POST /v1/erasure/retention", s.retention)
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
	if err := s.vault.Erase(r.Context(), id, subj); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"subject": subj, "erased": true})
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
