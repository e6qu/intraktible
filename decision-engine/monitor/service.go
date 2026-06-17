// SPDX-License-Identifier: AGPL-3.0-or-later

package monitor

import (
	"net/http"

	"github.com/e6qu/intraktible/decision-engine/analytics"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Service is the monitor HTTP surface (imperative shell): define/list/delete
// monitors on a flow, evaluating each rule's live status against the metrics
// projection at read time.
type Service struct {
	cmd   *Handler
	store store.Store
}

// New wires the monitor write side and read model to HTTP.
func New(cmd *Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the monitor endpoints (under a flow).
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/flows/{flow_id}/monitors", s.define)
	mux.HandleFunc("GET /v1/flows/{flow_id}/monitors", s.list)
	mux.HandleFunc("DELETE /v1/flows/{flow_id}/monitors/{monitor_id}", s.delete)
}

type defineRequest struct {
	Metric      string  `json:"metric"`
	Op          string  `json:"op"`
	Threshold   float64 `json:"threshold"`
	Description string  `json:"description,omitempty"`
}

func (s *Service) define(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req defineRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	mid, e, err := s.cmd.Define(r.Context(), id, DefineCmd{
		FlowID: r.PathValue("flow_id"), Metric: req.Metric, Op: req.Op,
		Threshold: req.Threshold, Description: req.Description,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"monitor_id": mid, "event_id": e.ID, "seq": e.Seq})
}

func (s *Service) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	e, err := s.cmd.Delete(r.Context(), id, r.PathValue("flow_id"), r.PathValue("monitor_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"event_id": e.ID, "seq": e.Seq})
}

// monitorStatus is a stored rule joined with its live evaluation.
type monitorStatus struct {
	View
	Status Status `json:"status"`
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	flowID := r.PathValue("flow_id")
	rules, err := ListByFlow(r.Context(), s.store, id, flowID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	metrics := s.metricsFor(r, id, flowID)
	out := make([]monitorStatus, 0, len(rules))
	for _, v := range rules {
		out = append(out, monitorStatus{View: v, Status: Evaluate(metrics, v.Rule())})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"monitors": out})
}

// metricsFor reads the flow's metrics snapshot (a zero snapshot when none yet, so
// rules over an unused flow read as "no data" rather than erroring).
func (s *Service) metricsFor(r *http.Request, id identity.Identity, flowID string) analytics.FlowMetrics {
	m, _, err := analytics.Read(r.Context(), s.store, id, flowID)
	if err != nil {
		return analytics.FlowMetrics{}
	}
	return m
}
