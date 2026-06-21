// SPDX-License-Identifier: AGPL-3.0-or-later

// Package service is the Agent Manager's HTTP surface (imperative shell): define
// agents, run them, and read the registry + run log (monitoring).
package service

import (
	"encoding/json"
	"net/http"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/agent-manager/command"
	"github.com/e6qu/intraktible/agent-manager/domain"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Service wires the agent commands and read model to HTTP.
type Service struct {
	cmd     *command.Handler
	store   store.Store
	pricing agents.Pricing
}

// Option configures a Service.
type Option func(*Service)

// WithPricing supplies the per-model price table used to derive run cost in the
// run summary. Without it, the summary reports token usage but no cost.
func WithPricing(p agents.Pricing) Option { return func(s *Service) { s.pricing = p } }

// New builds the service.
func New(cmd *command.Handler, st store.Store, opts ...Option) *Service {
	s := &Service{cmd: cmd, store: st}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Routes registers the agent-management endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	httpx.Register(mux, []httpx.Route{
		{Method: "POST", Pattern: "/v1/agents", Handler: s.defineAgent},
		{Method: "GET", Pattern: "/v1/agents", Handler: s.listAgents},
		{Method: "GET", Pattern: "/v1/agents/{name}", Handler: s.getAgent},
		{Method: "POST", Pattern: "/v1/agents/{name}/run", Handler: s.runAgent},
		{Method: "GET", Pattern: "/v1/agents/{name}/run/stream", Handler: s.runStreamSSE},
		{Method: "GET", Pattern: "/v1/agents/{name}/run/ws", Handler: s.runStreamWS},
		{Method: "GET", Pattern: "/v1/agents/{name}/runs", Handler: s.listAgentRuns},
		{Method: "POST", Pattern: "/v1/agents/{name}/runs/{run_id}/escalate", Handler: s.escalateRun},
		{Method: "GET", Pattern: "/v1/agent-runs", Handler: s.listRuns},
		{Method: "GET", Pattern: "/v1/agent-runs/summary", Handler: s.runSummary},
		{Method: "GET", Pattern: "/v1/agent-runs/{run_id}", Handler: s.getRun},
	})
}

type agentRequest struct {
	Name     string          `json:"name"`
	Provider string          `json:"provider,omitempty"`
	Model    string          `json:"model,omitempty"`
	System   string          `json:"system,omitempty"`
	Schema   json.RawMessage `json:"schema,omitempty"`
	Tools    []string        `json:"tools,omitempty"`
}

func (s *Service) defineAgent(w http.ResponseWriter, r *http.Request) {
	var req agentRequest
	httpx.Emit(w, r, &req, func(id identity.Identity) (eventlog.Envelope, error) {
		agent := domain.DefineAgent{Name: req.Name, Provider: req.Provider, Model: req.Model, System: req.System, Schema: req.Schema, Tools: req.Tools}
		return s.cmd.DefineAgent(r.Context(), id, agent)
	})
}

func (s *Service) runAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req struct {
		Prompt string `json:"prompt"`
		Async  bool   `json:"async,omitempty"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	name := r.PathValue("name")
	// Async: queue the run and return 202 immediately; the caller polls the run.
	if req.Async {
		runID, err := s.cmd.StartRun(r.Context(), id, name, req.Prompt)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err)
			return
		}
		httpx.JSON(w, http.StatusAccepted, map[string]any{"run_id": runID, "status": "running"})
		return
	}
	res, err := s.cmd.RunAgent(r.Context(), id, name, req.Prompt)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"run_id": res.RunID, "status": res.Status,
		"text": res.Text, "structured": res.Structured, "error": res.Error,
	})
}

func (s *Service) listAgents(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	recs, err := agents.List(r.Context(), s.store, id)
	httpx.WriteList(w, "agents", recs, err)
}

func (s *Service) getAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	a, found, err := agents.Read(r.Context(), s.store, id, r.PathValue("name"))
	httpx.WriteOne(w, a, found, err, "agent not found")
}

func (s *Service) listAgentRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	recs, err := agents.ListRuns(r.Context(), s.store, id, r.PathValue("name"))
	httpx.WriteList(w, "runs", recs, err)
}

func (s *Service) getRun(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	run, found, err := agents.GetRun(r.Context(), s.store, id, r.PathValue("run_id"))
	httpx.WriteOne(w, run, found, err, "run not found")
}

func (s *Service) escalateRun(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req struct {
		CompanyName string `json:"company_name"`
		CaseType    string `json:"case_type"`
		SLADays     int    `json:"sla_days"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	caseID, _, err := s.cmd.EscalateRun(r.Context(), id, domain.EscalateRun{
		RunID: r.PathValue("run_id"), CompanyName: req.CompanyName, CaseType: req.CaseType, SLADays: req.SLADays,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"case_id": caseID})
}

func (s *Service) listRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	recs, err := agents.ListRuns(r.Context(), s.store, id, "")
	httpx.WriteList(w, "runs", recs, err)
}

func (s *Service) runSummary(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	recs, err := agents.ListRuns(r.Context(), s.store, id, "")
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, s.pricing.Cost(agents.SummarizeRuns(recs)))
}
