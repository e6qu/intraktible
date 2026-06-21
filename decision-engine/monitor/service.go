// SPDX-License-Identifier: AGPL-3.0-or-later

package monitor

import (
	"context"
	"fmt"
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
	Deliver(ctx context.Context, id identity.Identity, reason string, payload any) (notify.DeliverySummary, error)
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
	mux.HandleFunc("POST /v1/flows/{flow_id}/baseline", s.captureBaseline)
	mux.HandleFunc("GET /v1/flows/{flow_id}/drift", s.drift)
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
	snap, err := LoadSnapshot(r.Context(), s.store, id, flowID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]monitorStatus, 0, len(rules))
	for _, v := range rules {
		out = append(out, monitorStatus{View: v, Status: Evaluate(snap, v.Rule())})
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

func firedFrom(v View, st Status) firedMonitor {
	return firedMonitor{
		MonitorID: v.MonitorID, Metric: v.Metric, Op: v.Op,
		Threshold: v.Threshold, Actual: st.Actual, Description: v.Description,
	}
}

// check evaluates a flow's monitors and pushes the firing ones to every active
// webhook. It is the pull-based alerting trigger (the scheduler does this on a
// timer): it returns the firing set and the per-webhook delivery outcomes.
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
	snap, err := LoadSnapshot(r.Context(), s.store, id, flowID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	fired := make([]firedMonitor, 0)
	for _, v := range rules {
		if st := Evaluate(snap, v.Rule()); st.Firing {
			fired = append(fired, firedFrom(v, st))
		}
	}
	resp := map[string]any{"flow_id": flowID, "checked": len(rules), "fired": fired}
	if len(fired) > 0 && s.notifier != nil {
		payload := map[string]any{"flow_id": flowID, "checked_at": time.Now().UTC(), "fired": fired}
		// Evaluation succeeded, so report the delivery outcomes rather than 500-ing the
		// check: surface the per-webhook results and flag delivery_failed when not every
		// webhook accepted (any retryable/permanent failure, or a real Deliver error).
		summary, derr := s.notifier.Deliver(r.Context(), id, "monitor check", payload)
		resp["deliveries"] = summary.Results
		if derr != nil || summary.Accepted < len(summary.Results) {
			resp["delivery_failed"] = true
		}
	}
	httpx.JSON(w, http.StatusOK, resp)
}

// captureBaseline snapshots the flow's current disposition distribution as the
// drift reference.
func (s *Service) captureBaseline(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	flowID := r.PathValue("flow_id")
	m, _, err := analytics.Read(r.Context(), s.store, id, flowID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	base, ok := DistributionOf(m)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("monitor: no dispositioned decisions to baseline yet"))
		return
	}
	e, err := s.cmd.CaptureBaseline(r.Context(), id, flowID, base)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"baseline": base, "event_id": e.ID, "seq": e.Seq})
}

// drift reports the current distribution vs the captured baseline, per bucket.
func (s *Service) drift(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	snap, err := LoadSnapshot(r.Context(), s.store, id, r.PathValue("flow_id"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ComputeDrift(snap))
}
