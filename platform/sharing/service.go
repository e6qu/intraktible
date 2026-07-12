// SPDX-License-Identifier: AGPL-3.0-or-later

package sharing

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// Service is the GLBA sharing opt-out HTTP surface: record a subject's opt-out (or
// rescind it) and query it. Reads are viewer-level; writes are operator-level (the
// election is recorded by the institution's staff), enforced by the route policy.
type Service struct {
	cmd   *Handler
	store store.Store
}

// New wires the sharing write side and read model to HTTP.
func New(cmd *Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the sharing endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	// Opt-out and opt-in (rescind) are mirror elections sharing one handler that
	// dispatches on the path — the record differs only in direction.
	mux.HandleFunc("POST /v1/sharing/opt-out", s.elect)
	mux.HandleFunc("POST /v1/sharing/opt-in", s.elect)
	// The subject is an opaque "type/id" string, so it is a query parameter, not a
	// path segment (a slash in a segment would misroute).
	mux.HandleFunc("GET /v1/sharing", s.status)
	mux.HandleFunc("GET /v1/sharing/records", s.listAll)
}

type optOutRequest struct {
	Subject string `json:"subject"`
	Reason  string `json:"reason,omitempty"`
}

func (s *Service) elect(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req optOutRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	var (
		e   eventlog.Envelope
		err error
	)
	if strings.HasSuffix(r.URL.Path, "/opt-in") {
		e, err = s.cmd.Rescind(r.Context(), id, req.Subject)
	} else {
		e, err = s.cmd.OptOut(r.Context(), id, req.Subject, req.Reason)
	}
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"event_id": e.ID, "seq": e.Seq})
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
	rec, found, err := Get(r.Context(), s.store, id, subject)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.JSON(w, http.StatusOK, map[string]any{"subject": subject, "opted_out": false})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"opted_out": rec.OptedOut, "record": rec})
}

func (s *Service) listAll(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	records, err := ListAll(r.Context(), s.store, id)
	httpx.WriteList(w, "records", records, err)
}
