// SPDX-License-Identifier: AGPL-3.0-or-later

package consent

import (
	"fmt"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// Service is the consent HTTP surface: record a subject's consent grant/withdrawal
// and query it. Reads are viewer-level, writes runtime-operator level (consent is
// captured during onboarding), enforced by the default route policy.
type Service struct {
	cmd   *Handler
	store store.Store
	now   func() time.Time
}

// New wires the consent write side and read model to HTTP.
func New(cmd *Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st, now: func() time.Time { return time.Now().UTC() }}
}

// Routes registers the consent endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/consent/grant", s.grant)
	mux.HandleFunc("POST /v1/consent/withdraw", s.withdraw)
	// The subject is an opaque string that may contain any character (the decide
	// integration keys it as "type/id"), so it is a query parameter, not a path
	// segment — a slash in a path segment would misroute.
	mux.HandleFunc("GET /v1/consent", s.list)
	mux.HandleFunc("GET /v1/consent/status", s.status)
}

type grantRequest struct {
	Subject   string      `json:"subject"`
	Purpose   string      `json:"purpose"`
	Basis     LawfulBasis `json:"basis,omitempty"`
	ExpiresAt *time.Time  `json:"expires_at,omitempty"`
}

func (s *Service) grant(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req grantRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	e, err := s.cmd.Grant(r.Context(), id, req.Subject, req.Purpose, req.Basis, req.ExpiresAt)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"event_id": e.ID, "seq": e.Seq})
}

type withdrawRequest struct {
	Subject string `json:"subject"`
	Purpose string `json:"purpose"`
	Reason  string `json:"reason,omitempty"`
}

func (s *Service) withdraw(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req withdrawRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	e, err := s.cmd.Withdraw(r.Context(), id, req.Subject, req.Purpose, req.Reason)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"event_id": e.ID, "seq": e.Seq})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	subject := r.URL.Query().Get("subject")
	if subject == "" {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("subject query parameter is required"))
		return
	}
	records, err := List(r.Context(), s.store, id, subject)
	httpx.WriteList(w, "consents", records, err)
}

// status returns whether a (subject, purpose) has active consent as of now, plus the
// full record.
func (s *Service) status(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	subject, purpose := r.URL.Query().Get("subject"), r.URL.Query().Get("purpose")
	if subject == "" || purpose == "" {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("subject and purpose query parameters are required"))
		return
	}
	rec, found, err := Get(r.Context(), s.store, id, subject, purpose)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.JSON(w, http.StatusOK, map[string]any{"subject": subject, "purpose": purpose, "active": false})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"active": rec.Active(s.now()), "record": rec})
}
