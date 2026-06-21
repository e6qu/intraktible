// SPDX-License-Identifier: AGPL-3.0-or-later

package mrm

import (
	"net/http"
	"time"

	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// Service exposes the model-risk report. It is read-only (no command side) and
// admin-gated by the route policy, since the report inventories the whole tenant's
// decision logic.
type Service struct {
	store store.Store
	now   func() time.Time
}

// New builds the MRM service over the read store.
func New(st store.Store) *Service {
	return &Service{store: st, now: func() time.Time { return time.Now().UTC() }}
}

// Routes registers the report endpoint.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/mrm/report", s.report)
}

// report builds the live MRM report and renders it as JSON (default), CSV
// (?format=csv — the inventory spreadsheet), or Markdown (?format=md — the filed
// document).
func (s *Service) report(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	rep, err := Build(r.Context(), s.store, id, s.now())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	switch r.URL.Query().Get("format") {
	case "csv":
		doc, err := CSV(rep)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err)
			return
		}
		httpx.Download(w, "text/csv; charset=utf-8", "mrm-inventory.csv", doc)
	case "md", "markdown":
		httpx.Download(w, "text/markdown; charset=utf-8", "mrm-report.md", Markdown(rep))
	default:
		httpx.JSON(w, http.StatusOK, rep)
	}
}
