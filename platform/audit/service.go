// SPDX-License-Identifier: AGPL-3.0-or-later

package audit

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// Service is the audit surface's HTTP shell: a tenant-scoped read over the indexed
// audit projection.
type Service struct {
	store store.Store
}

// New builds the audit service over the indexed audit projection (register the
// audit.Projector so the store is populated).
func New(s store.Store) *Service { return &Service{store: s} }

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
	// CSV export covers the whole filtered set (capped at MaxLimit), independent of
	// the on-screen page.
	if r.URL.Query().Get("format") == "csv" {
		entries, err := Read(r.Context(), s.store, id, Query{
			Stream: q.Stream, Actor: q.Actor, Type: q.Type, Resource: q.Resource,
			Since: q.Since, Until: q.Until, ExcludeType: q.ExcludeType, Limit: MaxLimit,
		})
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err)
			return
		}
		doc, err := CSV(entries)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err)
			return
		}
		httpx.Download(w, "text/csv; charset=utf-8", "audit.csv", doc)
		return
	}
	page, err := ReadPage(r.Context(), s.store, id, q)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func parseQuery(r *http.Request) (Query, error) {
	qp := r.URL.Query()
	q := Query{
		Stream:      qp.Get("stream"),
		Actor:       qp.Get("actor"),
		Type:        qp.Get("type"),
		Resource:    qp.Get("resource"),
		ExcludeType: qp.Get("exclude_type"),
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
	if v := qp.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return Query{}, fmt.Errorf("invalid offset %q", v)
		}
		q.Offset = n
	}
	return q, nil
}
