// SPDX-License-Identifier: AGPL-3.0-or-later

package population

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Service exposes durable population job control and result downloads.
type Service struct {
	handler *Handler
	store   store.Store
	now     func() time.Time
}

func New(handler *Handler, st store.Store) *Service {
	return &Service{
		handler: handler, store: st,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) WithNow(now func() time.Time) *Service {
	s.now = now
	return s
}

func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/population-jobs", s.create)
	mux.HandleFunc("GET /v1/population-jobs", s.list)
	mux.HandleFunc("GET /v1/population-jobs/{job_id}", s.get)
	mux.HandleFunc("GET /v1/population-jobs/{job_id}/results", s.results)
	mux.HandleFunc("POST /v1/population-jobs/{job_id}/pause", s.pause)
	mux.HandleFunc("POST /v1/population-jobs/{job_id}/resume", s.resume)
	mux.HandleFunc("POST /v1/population-jobs/{job_id}/cancel", s.cancel)
	mux.HandleFunc("POST /v1/population-jobs/{job_id}/retry", s.retry)
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var command CreateCommand
	if err := httpx.DecodeJSON(r, &command); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if !allowEnvironment(w, r, command.Environment) {
		return
	}
	payload, event, err := s.handler.Create(
		r.Context(), id, command, r.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writePopulationError(w, err)
		return
	}
	status := http.StatusAccepted
	if event.Seq == 0 {
		status = http.StatusOK
	}
	httpx.JSON(w, status, map[string]any{
		"job_id": payload.JobID, "state": StateQueued,
		"manifest_hash": payload.ManifestHash, "total": len(payload.Manifest.Items),
		"event_id": event.ID, "seq": event.Seq,
	})
}

type summary struct {
	JobID        string    `json:"job_id"`
	Kind         Kind      `json:"kind"`
	Slug         string    `json:"slug"`
	Environment  string    `json:"environment"`
	State        State     `json:"state"`
	Total        int       `json:"total"`
	Pending      int       `json:"pending"`
	Running      int       `json:"running"`
	Succeeded    int       `json:"succeeded"`
	Failed       int       `json:"failed"`
	ManifestHash string    `json:"manifest_hash"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func summarize(view View) summary {
	return summary{
		JobID: view.JobID, Kind: view.Manifest.Kind, Slug: view.Manifest.Slug,
		Environment: view.Manifest.Environment, State: view.State,
		Total: view.Total, Pending: view.Pending, Running: view.Running,
		Succeeded: view.Succeeded, Failed: view.Failed,
		ManifestHash: view.ManifestHash, CreatedAt: view.CreatedAt,
		UpdatedAt: view.UpdatedAt, ExpiresAt: view.ExpiresAt,
	}
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	views, err := List(r.Context(), s.store, id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	items := make([]summary, len(views))
	for i := range views {
		items[i] = summarize(views[i])
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"jobs": items})
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	_, view, ok := s.read(w, r)
	if !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

func (s *Service) results(w http.ResponseWriter, r *http.Request) {
	_, view, ok := s.read(w, r)
	if !ok {
		return
	}
	if view.State == StateExpired || (!view.ExpiresAt.IsZero() && !s.now().Before(view.ExpiresAt)) {
		httpx.Error(w, http.StatusGone, fmt.Errorf("population: result retention elapsed"))
		return
	}
	if !view.State.terminal() {
		httpx.Error(w, http.StatusConflict, fmt.Errorf("population: job is %s; results are final only after completion", view.State))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="population-%s.ndjson"`, view.JobID))
	encoder := json.NewEncoder(w)
	for _, item := range view.Items {
		row := struct {
			Index       int            `json:"index"`
			EntityID    string         `json:"entity_id,omitempty"`
			DecisionID  string         `json:"decision_id,omitempty"`
			Status      string         `json:"status"`
			Output      map[string]any `json:"output,omitempty"`
			Disposition string         `json:"disposition,omitempty"`
			Error       string         `json:"error,omitempty"`
		}{
			Index: item.Index, EntityID: view.Manifest.Items[item.Index].Input.EntityID,
			DecisionID: item.DecisionID, Status: item.Status, Output: item.Output,
			Disposition: item.Disposition, Error: item.Error,
		}
		if item.State == "failed" {
			row.Status = "failed"
		}
		if err := encoder.Encode(row); err != nil {
			return
		}
	}
}

type reasonRequest struct {
	Reason string `json:"reason"`
}

func (s *Service) pause(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, s.handler.Pause)
}

func (s *Service) resume(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, s.handler.Resume)
}

func (s *Service) cancel(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, s.handler.Cancel)
}

func (s *Service) transition(
	w http.ResponseWriter,
	r *http.Request,
	command func(context.Context, identity.Identity, string, string) (eventlog.Envelope, error),
) {
	id, view, ok := s.read(w, r)
	if !ok || !allowEnvironment(w, r, view.Manifest.Environment) {
		return
	}
	var request reasonRequest
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &request); err != nil {
			httpx.Error(w, http.StatusBadRequest, err)
			return
		}
	}
	event, err := command(r.Context(), id, view.JobID, request.Reason)
	writePopulationEvent(w, event, err)
}

func (s *Service) retry(w http.ResponseWriter, r *http.Request) {
	id, view, ok := s.read(w, r)
	if !ok || !allowEnvironment(w, r, view.Manifest.Environment) {
		return
	}
	var request struct {
		Indices []int `json:"indices,omitempty"`
	}
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &request); err != nil {
			httpx.Error(w, http.StatusBadRequest, err)
			return
		}
	}
	event, err := s.handler.Retry(r.Context(), id, view.JobID, request.Indices)
	writePopulationEvent(w, event, err)
}

func (s *Service) read(w http.ResponseWriter, r *http.Request) (identity.Identity, View, bool) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return identity.Identity{}, View{}, false
	}
	view, found, err := Read(r.Context(), s.store, id, r.PathValue("job_id"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return identity.Identity{}, View{}, false
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("population: job not found"))
		return identity.Identity{}, View{}, false
	}
	return id, view, true
}

func allowEnvironment(w http.ResponseWriter, r *http.Request, environment string) bool {
	scope, ok := httpx.Scope(r.Context())
	if !ok || !scope.Allows(environment) {
		httpx.Error(w, http.StatusForbidden, fmt.Errorf("scope %q does not permit environment %q", scope, environment))
		return false
	}
	return true
}

func writePopulationEvent(w http.ResponseWriter, event eventlog.Envelope, err error) {
	if err != nil {
		writePopulationError(w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"event_id": event.ID, "seq": event.Seq})
}

func writePopulationError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, eventlog.ErrConflict) {
		status = http.StatusConflict
	}
	httpx.Error(w, status, err)
}
