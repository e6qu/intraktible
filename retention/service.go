// SPDX-License-Identifier: AGPL-3.0-or-later

package retention

import (
	"fmt"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// Service exposes a subject's record-retention status (read-only).
type Service struct {
	store store.Store
	now   func() time.Time
}

// New wires the retention read model to HTTP.
func New(st store.Store) *Service {
	return &Service{store: st, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the clock (deterministic tests).
func (s *Service) WithNow(now func() time.Time) *Service {
	s.now = now
	return s
}

// Routes registers the retention endpoint.
func (s *Service) Routes(mux *http.ServeMux) {
	// The subject is an opaque "type/id" string, so it is a query parameter.
	mux.HandleFunc("GET /v1/retention", s.status)
}

func (s *Service) status(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	subject := r.URL.Query().Get("subject")
	if subject == "" {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("subject query parameter is required"))
		return
	}
	st, err := StatusFor(r.Context(), s.store, id, subject, s.now())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, st)
}
