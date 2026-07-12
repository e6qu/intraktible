// SPDX-License-Identifier: AGPL-3.0-or-later

package registers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/fairlending"
	"github.com/e6qu/intraktible/platform/consent"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/reconsideration"
)

// Service serves the compliance registers as downloadable CSV / Markdown. Reads only;
// each register is folded from an existing tenant-wide read model.
type Service struct {
	store store.Store
	now   func() time.Time
}

// New wires the registers read model to HTTP.
func New(st store.Store) *Service {
	return &Service{store: st, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the clock stamped into the generated-at line (deterministic tests).
func (s *Service) WithNow(now func() time.Time) *Service {
	s.now = now
	return s
}

// Routes registers the compliance-register endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/compliance/registers/{register}", s.export)
}

// export renders one register as CSV (default) or Markdown (?format=md). An unknown
// register name is a 404; the format defaults to CSV.
func (s *Service) export(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	name := r.PathValue("register")
	markdown := r.URL.Query().Get("format") == "md" || r.URL.Query().Get("format") == "markdown"
	generated := s.now().Format("2006-01-02 15:04 MST")

	var body string
	var err error
	switch name {
	case "adverse-actions":
		items, e := fairlending.ListIssuances(r.Context(), s.store, id)
		if e != nil {
			httpx.Error(w, http.StatusInternalServerError, e)
			return
		}
		if markdown {
			body = AdverseActionMarkdown(items, generated)
		} else {
			body, err = AdverseActionCSV(items)
		}
	case "reconsiderations":
		items, e := reconsideration.List(r.Context(), s.store, id)
		if e != nil {
			httpx.Error(w, http.StatusInternalServerError, e)
			return
		}
		if markdown {
			body = ReconsiderationMarkdown(items, generated)
		} else {
			body, err = ReconsiderationCSV(items)
		}
	case "consent":
		items, e := consent.ListAll(r.Context(), s.store, id)
		if e != nil {
			httpx.Error(w, http.StatusInternalServerError, e)
			return
		}
		if markdown {
			body = ConsentMarkdown(items, generated)
		} else {
			body, err = ConsentCSV(items)
		}
	default:
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("unknown register %q (adverse-actions, reconsiderations, consent)", name))
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if markdown {
		httpx.Download(w, "text/markdown; charset=utf-8", name+"-register.md", body)
		return
	}
	httpx.Download(w, "text/csv; charset=utf-8", name+"-register.csv", body)
}
