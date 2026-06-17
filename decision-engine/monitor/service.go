// SPDX-License-Identifier: AGPL-3.0-or-later

package monitor

import (
	"context"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/decision-engine/analytics"
	"github.com/e6qu/intraktible/decision-engine/notify"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// notifier is the subset of *notify.Notifier the check endpoint needs (so the
// monitor service depends on a small contract, and tests can omit delivery).
type notifier interface {
	Deliver(ctx context.Context, id identity.Identity, reason string, payload any) ([]notify.DeliveryResult, error)
}

// Service is the monitor HTTP surface (imperative shell): define/list/delete
// monitors on a flow, evaluating each rule's live status against the metrics
// projection at read time, and pushing firing rules to webhooks on check.
type Service struct {
	cmd      *Handler
	store    store.Store
	notifier notifier // nil when no notification channel is wired
}

// New wires the monitor write side and read model to HTTP. notify may be nil, in
// which case a check evaluates but does not deliver.
func New(cmd *Handler, st store.Store, notify notifier) *Service {
	return &Service{cmd: cmd, store: st, notifier: notify}
}

// Routes registers the monitor endpoints (under a flow).
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/flows/{flow_id}/monitors", s.define)
	mux.HandleFunc("GET /v1/flows/{flow_id}/monitors", s.list)
	mux.HandleFunc("DELETE /v1/flows/{flow_id}/monitors/{monitor_id}", s.delete)
	mux.HandleFunc("POST /v1/flows/{flow_id}/monitors/check", s.check)
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

// firedMonitor is a firing rule in a check payload/response.
type firedMonitor struct {
	MonitorID   string  `json:"monitor_id"`
	Metric      string  `json:"metric"`
	Op          string  `json:"op"`
	Threshold   float64 `json:"threshold"`
	Actual      float64 `json:"actual"`
	Description string  `json:"description,omitempty"`
}

// check evaluates a flow's monitors and pushes the firing ones to every active
// webhook. It is the pull-based alerting trigger (wire it to cron / a scheduler):
// it returns the firing set and the per-webhook delivery outcomes.
func (s *Service) check(w http.ResponseWriter, r *http.Request) {
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
	fired := make([]firedMonitor, 0)
	for _, v := range rules {
		if st := Evaluate(metrics, v.Rule()); st.Firing {
			fired = append(fired, firedMonitor{
				MonitorID: v.MonitorID, Metric: v.Metric, Op: v.Op,
				Threshold: v.Threshold, Actual: st.Actual, Description: v.Description,
			})
		}
	}
	resp := map[string]any{"flow_id": flowID, "checked": len(rules), "fired": fired}
	if len(fired) > 0 && s.notifier != nil {
		payload := map[string]any{"flow_id": flowID, "checked_at": time.Now().UTC(), "fired": fired}
		deliveries, derr := s.notifier.Deliver(r.Context(), id, "monitor check", payload)
		if derr != nil {
			httpx.Error(w, http.StatusInternalServerError, derr)
			return
		}
		resp["deliveries"] = deliveries
	}
	httpx.JSON(w, http.StatusOK, resp)
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
