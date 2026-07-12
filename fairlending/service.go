// SPDX-License-Identifier: AGPL-3.0-or-later

package fairlending

import (
	"fmt"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// Service exposes the disparate-impact report. It is read-only (no command side)
// and admin-gated by the route policy, since it breaks a flow's whole decision
// population down by a protected-class attribute.
type Service struct {
	store store.Store
	now   func() time.Time
}

// New builds the fair-lending service over the read store.
func New(st store.Store) *Service {
	return &Service{store: st, now: func() time.Time { return time.Now().UTC() }}
}

// Routes registers the report endpoint.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/fairlending/report", s.report)
}

// report builds the disparate-impact report for the requested flow and attribute
// and renders it as JSON (default), CSV (?format=csv), or Markdown (?format=md).
// The flow and attribute query params are required; a favorable outcome other than
// approve/decline/refer is rejected rather than silently defaulted.
func (s *Service) report(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	p := Params{
		FlowID:      q.Get("flow"),
		Attribute:   q.Get("attribute"),
		Environment: q.Get("env"),
	}
	if p.FlowID == "" {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("flow is required"))
		return
	}
	if p.Attribute == "" {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("attribute is required"))
		return
	}
	if fav := q.Get("favorable"); fav != "" {
		d := policy.Disposition(fav)
		if d != policy.Approve && d != policy.Decline && d != policy.Refer {
			httpx.Error(w, http.StatusBadRequest, fmt.Errorf("favorable must be approve, decline, or refer"))
			return
		}
		p.Favorable = d
	}

	rep, err := Build(r.Context(), s.store, id, p, s.now())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	switch q.Get("format") {
	case "csv":
		doc, err := CSV(rep)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err)
			return
		}
		httpx.Download(w, "text/csv; charset=utf-8", "disparate-impact.csv", doc)
	case "md", "markdown":
		httpx.Download(w, "text/markdown; charset=utf-8", "disparate-impact.md", Markdown(rep))
	default:
		httpx.JSON(w, http.StatusOK, rep)
	}
}
