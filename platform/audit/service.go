// SPDX-License-Identifier: AGPL-3.0-or-later

package audit

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
)

// Service is the audit surface's HTTP shell: a tenant-scoped read over the log.
type Service struct {
	log eventlog.Log
}

// New builds the audit service over the event log.
func New(log eventlog.Log) *Service { return &Service{log: log} }

// Routes registers the audit endpoint. It is a platform capability (independent
// of which product modules are enabled).
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/audit", s.list)
}

// list returns the tenant's audit trail, filterable by stream/actor/type/resource
// and an RFC3339 time range, newest-first. `?format=csv` exports it as a file.
//
//	GET /v1/audit?stream=&actor=&type=&resource=&since=&until=&limit=&format=
func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	q, err := parseQuery(r)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	entries, err := Read(r.Context(), s.log, id, q)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if r.URL.Query().Get("format") == "csv" {
		doc, err := CSV(entries)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err)
			return
		}
		httpx.Download(w, "text/csv; charset=utf-8", "audit.csv", doc)
		return
	}
	httpx.WriteList(w, "entries", entries, nil)
}

func parseQuery(r *http.Request) (Query, error) {
	qp := r.URL.Query()
	q := Query{
		Stream:   qp.Get("stream"),
		Actor:    qp.Get("actor"),
		Type:     qp.Get("type"),
		Resource: qp.Get("resource"),
	}
	for _, b := range []struct {
		key string
		dst *time.Time
	}{{"since", &q.Since}, {"until", &q.Until}} {
		if v := qp.Get(b.key); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return Query{}, fmt.Errorf("invalid %s (want RFC3339): %w", b.key, err)
			}
			*b.dst = t
		}
	}
	if v := qp.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return Query{}, fmt.Errorf("invalid limit %q", v)
		}
		q.Limit = n
	}
	return q, nil
}
