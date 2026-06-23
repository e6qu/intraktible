// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/agent-manager/command"
	"github.com/e6qu/intraktible/agent-manager/service"
	caseevents "github.com/e6qu/intraktible/case-manager/events"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
)

func start(t *testing.T) *testutil.API {
	t.Helper()
	api, _ := startWithLog(t)
	return api
}

// startWithLog is start but also returns the shared log, so a test can inspect the
// events a handler emitted (e.g. the case-open event an escalation writes).
func startWithLog(t *testing.T) (*testutil.API, eventlog.Log) {
	t.Helper()
	log, st := testutil.NewLogStore(t)
	reg := ai.NewRegistry()
	reg.Register(ai.Stub{})
	svc := service.New(command.NewHandler(log, st, reg), st)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	return testutil.StartAPI(t, log, st, "test-key", id, svc.Routes, agents.Projector{}), log
}

func TestAgentAPIEndToEnd(t *testing.T) {
	api := start(t)

	api.Request(t, http.MethodPost, "/v1/agents",
		map[string]any{"name": "triage", "system": "be terse", "tools": []string{"lookup"}},
		http.StatusAccepted, nil)

	// The agent appears in the registry (async projection).
	if !testutil.Eventually(t, func() bool {
		var a agents.AgentView
		api.Request(t, http.MethodGet, "/v1/agents/triage", nil, http.StatusOK, &a)
		return a.Name == "triage" && a.System == "be terse"
	}) {
		t.Fatal("agent never appeared in the registry")
	}

	// Run it: the stub echoes the prompt.
	var run struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
		Text   string `json:"text"`
	}
	api.Request(t, http.MethodPost, "/v1/agents/triage/run",
		map[string]any{"prompt": "hello"}, http.StatusOK, &run)
	if run.RunID == "" || run.Status != "completed" || run.Text != "stub: hello" {
		t.Fatalf("run response: %+v", run)
	}

	// The run is in the agent's run log and fetchable by id (monitoring).
	if !testutil.Eventually(t, func() bool {
		var list struct {
			Runs []agents.RunView `json:"runs"`
		}
		api.Request(t, http.MethodGet, "/v1/agents/triage/runs", nil, http.StatusOK, &list)
		return len(list.Runs) == 1 && list.Runs[0].RunID == run.RunID
	}) {
		t.Fatal("run never appeared in the run log")
	}
	var got agents.RunView
	api.Request(t, http.MethodGet, "/v1/agent-runs/"+run.RunID, nil, http.StatusOK, &got)
	if got.Agent != "triage" || got.Status != "completed" {
		t.Fatalf("run by id: %+v", got)
	}

	// Escalate the run to a case (human-in-the-loop).
	var esc struct {
		CaseID string `json:"case_id"`
	}
	api.Request(t, http.MethodPost, "/v1/agents/triage/runs/"+run.RunID+"/escalate",
		map[string]any{"company_name": "Acme Corp", "case_type": "aml", "sla_days": 3}, http.StatusAccepted, &esc)
	if esc.CaseID == "" {
		t.Fatal("escalation returned no case id")
	}

	// Run monitoring summary.
	var sum agents.RunSummary
	api.Request(t, http.MethodGet, "/v1/agent-runs/summary", nil, http.StatusOK, &sum)
	if sum.Total != 1 || sum.Completed != 1 || sum.ByAgent["triage"] != 1 {
		t.Fatalf("run summary: %+v", sum)
	}
}

func TestAgentAPIValidationAndAuth(t *testing.T) {
	api := start(t)

	// Missing name -> 400.
	api.Request(t, http.MethodPost, "/v1/agents", map[string]any{"system": "x"}, http.StatusBadRequest, nil)
	// Running an unknown agent -> 400.
	api.Request(t, http.MethodPost, "/v1/agents/ghost/run", map[string]any{"prompt": "x"}, http.StatusBadRequest, nil)
	// Unknown agent / run -> 404.
	api.Request(t, http.MethodGet, "/v1/agents/ghost", nil, http.StatusNotFound, nil)
	api.Request(t, http.MethodGet, "/v1/agent-runs/ghost", nil, http.StatusNotFound, nil)
	// Unauthenticated -> 401.
	resp, err := http.Get(api.Server.URL + "/v1/agents")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated -> %d, want 401", resp.StatusCode)
	}
}

// TestEscalateDefaultsCaseType proves an escalation with no case_type opens an
// agent_review case server-side, so the queue can route it without relying on the
// client to send a type.
func TestEscalateDefaultsCaseType(t *testing.T) {
	api, log := startWithLog(t)

	api.Request(t, http.MethodPost, "/v1/agents",
		map[string]any{"name": "triage", "system": "be terse"}, http.StatusAccepted, nil)
	var run struct {
		RunID string `json:"run_id"`
	}
	api.Request(t, http.MethodPost, "/v1/agents/triage/run",
		map[string]any{"prompt": "hello"}, http.StatusOK, &run)

	var esc struct {
		CaseID string `json:"case_id"`
	}
	// No case_type in the request.
	api.Request(t, http.MethodPost, "/v1/agents/triage/runs/"+run.RunID+"/escalate",
		map[string]any{"company_name": "Acme Corp", "sla_days": 3}, http.StatusAccepted, &esc)
	if esc.CaseID == "" {
		t.Fatal("escalation returned no case id")
	}

	// The emitted case-open event carries the server-side default case type.
	evs, err := log.Read(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range evs {
		if e.Type != caseevents.TypeReviewRequested {
			continue
		}
		var p caseevents.ReviewRequested
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.CaseID == esc.CaseID {
			found = true
			if p.CaseType != "agent_review" {
				t.Fatalf("case_type = %q, want agent_review", p.CaseType)
			}
		}
	}
	if !found {
		t.Fatalf("no ReviewRequested event for case %q", esc.CaseID)
	}
}
