// SPDX-License-Identifier: AGPL-3.0-or-later

package experiments

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// DeploymentRequester opens the governed deployment request used to promote a
// statistically valid winner.
type DeploymentRequester interface {
	RequestDeployment(context.Context, identity.Identity, domain.DeployVersion) (string, eventlog.Envelope, error)
}

// Service exposes experiment setup, governance, health, and analysis.
type Service struct {
	handler  *Handler
	store    store.Store
	promoter DeploymentRequester
}

func New(handler *Handler, st store.Store, promoter DeploymentRequester) *Service {
	return &Service{handler: handler, store: st, promoter: promoter}
}

func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/experiments", s.create)
	mux.HandleFunc("GET /v1/experiments", s.list)
	mux.HandleFunc("GET /v1/experiments/{experiment_id}", s.get)
	mux.HandleFunc("PUT /v1/experiments/{experiment_id}", s.update)
	mux.HandleFunc("POST /v1/experiments/{experiment_id}/start", s.start)
	mux.HandleFunc("POST /v1/experiments/{experiment_id}/launch-requests/{request_id}/approve", s.approve)
	mux.HandleFunc("POST /v1/experiments/{experiment_id}/launch-requests/{request_id}/reject", s.reject)
	mux.HandleFunc("POST /v1/experiments/{experiment_id}/pause", s.pause)
	mux.HandleFunc("POST /v1/experiments/{experiment_id}/resume", s.resume)
	mux.HandleFunc("POST /v1/experiments/{experiment_id}/complete", s.complete)
	mux.HandleFunc("POST /v1/experiments/{experiment_id}/cancel", s.cancel)
	mux.HandleFunc("GET /v1/experiments/{experiment_id}/analysis", s.analysis)
	mux.HandleFunc("GET /v1/experiments/{experiment_id}/exposures", s.exposures)
	mux.HandleFunc("POST /v1/experiments/{experiment_id}/promote", s.promote)
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var spec Spec
	if err := httpx.DecodeJSON(r, &spec); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if !allowEnvironment(w, r, spec.Environment) {
		return
	}
	experimentID, event, err := s.handler.Create(r.Context(), id, spec)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"experiment_id": experimentID, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	items, err := List(r.Context(), s.store, id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"experiments": items})
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	view, found, err := Read(r.Context(), s.store, id, r.PathValue("experiment_id"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("experiments: experiment not found"))
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

func (s *Service) update(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var spec Spec
	if err := httpx.DecodeJSON(r, &spec); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if !allowEnvironment(w, r, spec.Environment) {
		return
	}
	event, err := s.handler.Update(r.Context(), id, r.PathValue("experiment_id"), spec)
	writeEvent(w, event, err)
}

func (s *Service) start(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok || !s.allowExperimentEnvironment(w, r, id) {
		return
	}
	requestID, event, err := s.handler.Start(r.Context(), id, r.PathValue("experiment_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	status := "running"
	if requestID != "" {
		status = "pending_launch"
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"status": status, "request_id": requestID, "event_id": event.ID, "seq": event.Seq,
	})
}

type reasonRequest struct {
	Reason string `json:"reason"`
}

func decodeReason(r *http.Request) (reasonRequest, error) {
	var request reasonRequest
	if r.ContentLength == 0 {
		return request, nil
	}
	if err := httpx.DecodeJSON(r, &request); err != nil {
		return reasonRequest{}, err
	}
	return request, nil
}

func (s *Service) approve(w http.ResponseWriter, r *http.Request) {
	s.decideLaunch(w, r, true)
}

func (s *Service) reject(w http.ResponseWriter, r *http.Request) {
	s.decideLaunch(w, r, false)
}

func (s *Service) decideLaunch(w http.ResponseWriter, r *http.Request, approve bool) {
	id, ok := httpx.Caller(w, r)
	if !ok || !s.allowExperimentEnvironment(w, r, id) {
		return
	}
	request, err := decodeReason(r)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	var event eventlog.Envelope
	if approve {
		event, err = s.handler.ApproveLaunch(
			r.Context(), id, r.PathValue("experiment_id"), r.PathValue("request_id"), request.Reason,
		)
	} else {
		event, err = s.handler.RejectLaunch(
			r.Context(), id, r.PathValue("experiment_id"), r.PathValue("request_id"), request.Reason,
		)
	}
	writeEvent(w, event, err)
}

func (s *Service) pause(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, s.handler.Pause)
}

func (s *Service) resume(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, s.handler.Resume)
}

func (s *Service) complete(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, s.handler.Complete)
}

func (s *Service) cancel(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, s.handler.Cancel)
}

func (s *Service) transition(
	w http.ResponseWriter,
	r *http.Request,
	command func(context.Context, identity.Identity, string, string) (eventlog.Envelope, error),
) {
	id, ok := httpx.Caller(w, r)
	if !ok || !s.allowExperimentEnvironment(w, r, id) {
		return
	}
	request, err := decodeReason(r)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	event, err := command(r.Context(), id, r.PathValue("experiment_id"), request.Reason)
	writeEvent(w, event, err)
}

func (s *Service) analysis(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	report, err := Analyze(r.Context(), s.store, id, r.PathValue("experiment_id"))
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "unknown experiment") {
			status = http.StatusNotFound
		}
		httpx.Error(w, status, err)
		return
	}
	httpx.JSON(w, http.StatusOK, report)
}

func (s *Service) exposures(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	view, found, err := Read(r.Context(), s.store, id, r.PathValue("experiment_id"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("experiments: experiment not found"))
		return
	}
	items, err := ListExposures(r.Context(), s.store, id, view.ExperimentID, view.Cohort)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"cohort": view.Cohort, "exposures": items})
}

func (s *Service) promote(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok || !s.allowExperimentEnvironment(w, r, id) {
		return
	}
	if s.promoter == nil {
		httpx.Error(w, http.StatusServiceUnavailable, fmt.Errorf("experiments: deployment promotion is not configured"))
		return
	}
	experimentID := r.PathValue("experiment_id")
	view, found, err := Read(r.Context(), s.store, id, experimentID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("experiments: experiment not found"))
		return
	}
	if view.State != StateCompleted {
		httpx.Error(w, http.StatusConflict, fmt.Errorf("experiments: complete the cohort before promotion"))
		return
	}
	report, err := Analyze(r.Context(), s.store, id, experimentID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if report.Status != StatusWinner || report.WinnerArmKey == "" {
		httpx.Error(w, http.StatusConflict, fmt.Errorf("experiments: analysis has no valid winner (%s)", report.Status))
		return
	}
	version := 0
	for _, arm := range view.Spec.Arms {
		if arm.Key == report.WinnerArmKey {
			version = arm.Version
		}
	}
	if version == 0 {
		httpx.Error(w, http.StatusInternalServerError, fmt.Errorf("experiments: winner arm is missing from cohort"))
		return
	}
	requestID, event, err := s.promoter.RequestDeployment(r.Context(), id, domain.DeployVersion{
		FlowID: view.Spec.FlowID, Environment: view.Spec.Environment, Version: version,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"request_id": requestID, "winner_arm_key": report.WinnerArmKey,
		"version": version, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) allowExperimentEnvironment(w http.ResponseWriter, r *http.Request, id identity.Identity) bool {
	view, found, err := Read(r.Context(), s.store, id, r.PathValue("experiment_id"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return false
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("experiments: experiment not found"))
		return false
	}
	return allowEnvironment(w, r, view.Spec.Environment)
}

func allowEnvironment(w http.ResponseWriter, r *http.Request, environment string) bool {
	scope, ok := httpx.Scope(r.Context())
	if !ok || !scope.Allows(environment) {
		httpx.Error(w, http.StatusForbidden, fmt.Errorf("scope %q does not permit environment %q", scope, environment))
		return false
	}
	return true
}

func writeEvent(w http.ResponseWriter, event eventlog.Envelope, err error) {
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, eventlog.ErrConflict) {
			status = http.StatusConflict
		}
		httpx.Error(w, status, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"event_id": event.ID, "seq": event.Seq})
}
