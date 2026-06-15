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
	cmd   *command.Handler
	store store.Store
}

// New builds the service.
func New(cmd *command.Handler, st store.Store) *Service {
	return &Service{cmd: cmd, store: st}
}

// Routes registers the agent-management endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	httpx.Register(mux, []httpx.Route{
		{Method: "POST", Pattern: "/v1/agents", Handler: s.defineAgent},
		{Method: "GET", Pattern: "/v1/agents", Handler: s.listAgents},
		{Method: "GET", Pattern: "/v1/agents/{name}", Handler: s.getAgent},
		{Method: "POST", Pattern: "/v1/agents/{name}/run", Handler: s.runAgent},
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
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.cmd.RunAgent(r.Context(), id, r.PathValue("name"), req.Prompt)
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
	httpx.JSON(w, http.StatusOK, agents.SummarizeRuns(recs))
}
