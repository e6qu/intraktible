// SPDX-License-Identifier: AGPL-3.0-or-later

package outcomes

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Service exposes general business actuals and their correction histories.
type Service struct {
	handler *Handler
	store   store.Store
}

func New(handler *Handler, st store.Store) *Service {
	return &Service{handler: handler, store: st}
}

func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/outcomes", s.record)
	mux.HandleFunc("GET /v1/outcomes", s.list)
	mux.HandleFunc("GET /v1/outcomes/{outcome_id}", s.get)
	mux.HandleFunc("POST /v1/outcomes/{outcome_id}/corrections", s.correct)
}

func (s *Service) record(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var command RecordCommand
	if err := httpx.DecodeJSON(r, &command); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	payload, event, err := s.handler.Record(
		r.Context(), id, command, r.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusAccepted
	if event.Seq == 0 {
		status = http.StatusOK
	}
	httpx.JSON(w, status, map[string]any{
		"outcome_id": payload.OutcomeID, "revision": payload.Revision,
		"event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) correct(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		Value                 float64   `json:"value"`
		Category              string    `json:"category,omitempty"`
		Censored              bool      `json:"censored,omitempty"`
		EventTime             time.Time `json:"event_time"`
		ObservationWindowDays int       `json:"observation_window_days,omitempty"`
		Source                Source    `json:"source"`
		LabelVersion          string    `json:"label_version"`
		Reason                string    `json:"reason"`
	}
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	payload, event, err := s.handler.Correct(
		r.Context(), id, r.PathValue("outcome_id"),
		RecordCommand{
			Value: request.Value, Category: request.Category, Censored: request.Censored,
			EventTime:             request.EventTime,
			ObservationWindowDays: request.ObservationWindowDays,
			Source:                request.Source, LabelVersion: request.LabelVersion,
		},
		request.Reason, r.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"outcome_id": payload.OutcomeID, "revision": payload.Revision,
		"event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	items, err := List(
		r.Context(), s.store, id,
		r.URL.Query().Get("decision_id"), r.URL.Query().Get("key"),
	)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"outcomes": items})
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	view, err := s.readOutcome(r.Context(), id, r.PathValue("outcome_id"))
	if err != nil {
		if errors.Is(err, errOutcomeNotFound) {
			httpx.Error(w, http.StatusNotFound, err)
			return
		}
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

var errOutcomeNotFound = errors.New("outcomes: outcome not found")

func (s *Service) readOutcome(ctx context.Context, id identity.Identity, outcomeID string) (View, error) {
	view, found, err := Read(ctx, s.store, id, outcomeID)
	if err != nil {
		return View{}, err
	}
	if !found {
		return View{}, errOutcomeNotFound
	}
	return view, nil
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, eventlog.ErrConflict) {
		status = http.StatusConflict
	}
	httpx.Error(w, status, err)
}
