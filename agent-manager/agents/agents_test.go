// SPDX-License-Identifier: AGPL-3.0-or-later

package agents_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// scriptedProvider asks to call the "bureau" tool on the first turn, then answers
// once it has seen a tool result.
type scriptedProvider struct{}

func (scriptedProvider) Name() string { return "scripted" }

func (scriptedProvider) Complete(_ context.Context, req ai.Request) (ai.Response, error) {
	if len(req.History) == 0 && len(req.Tools) > 0 {
		return ai.Response{ToolCalls: []ai.ToolCall{{
			ID: "c1", Name: "bureau", Arguments: json.RawMessage(`{"subject":"acme"}`),
		}}}, nil
	}
	// Echo the most recent tool result into the final answer.
	last := ""
	if n := len(req.History); n > 0 {
		last = req.History[n-1].Content
	}
	return ai.Response{Text: "answer using " + last, Model: "scripted"}, nil
}

// alwaysToolProvider never stops requesting a tool — exercises the step limit.
type alwaysToolProvider struct{}

func (alwaysToolProvider) Name() string { return "loopy" }
func (alwaysToolProvider) Complete(_ context.Context, _ ai.Request) (ai.Response, error) {
	return ai.Response{ToolCalls: []ai.ToolCall{{ID: "c", Name: "bureau", Arguments: json.RawMessage(`{}`)}}}, nil
}

// fakeToolbox resolves only the "bureau" tool and returns a fixed result.
type fakeToolbox struct{ calls int }

func (t *fakeToolbox) Spec(name string) (ai.Tool, bool) {
	return ai.Tool{Name: name, Parameters: json.RawMessage(`{"type":"object"}`)}, name == "bureau"
}

func (t *fakeToolbox) Call(_ context.Context, _ identity.Identity, name string, _ json.RawMessage) (json.RawMessage, error) {
	t.calls++
	return json.RawMessage(`{"risk":42}`), nil
}

func defineAgent(t *testing.T, s store.Store, id identity.Identity, v agents.AgentView) {
	t.Helper()
	v.Org, v.Workspace = id.Org, id.Workspace
	if err := store.PutDoc(context.Background(), s, agents.CollectionAgents, store.Key(id.Org, id.Workspace, v.Name), v); err != nil {
		t.Fatal(err)
	}
}

func TestInvokeWithToolsRunsTheLoop(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	reg := ai.NewRegistry()
	reg.Register(scriptedProvider{})
	defineAgent(t, s, id, agents.AgentView{Name: "checker", Provider: "scripted", Tools: []string{"bureau"}})

	tb := &fakeToolbox{}
	out, err := agents.InvokeWithTools(ctx, s, reg, tb, id, "checker", "assess acme")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "completed" {
		t.Fatalf("status = %q (err %q)", out.Status, out.Error)
	}
	if tb.calls != 1 || len(out.ToolCalls) != 1 || out.ToolCalls[0].Name != "bureau" {
		t.Fatalf("tool trace = %+v (calls %d)", out.ToolCalls, tb.calls)
	}
	if !strings.Contains(string(out.ToolCalls[0].Result), "42") {
		t.Fatalf("tool result not recorded: %+v", out.ToolCalls[0])
	}
	if !strings.Contains(out.Text, "risk") {
		t.Fatalf("final answer did not incorporate the tool result: %q", out.Text)
	}
}

func TestInvokeWithoutToolboxIsAPlainCompletion(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	reg := ai.NewRegistry()
	reg.Register(scriptedProvider{})
	defineAgent(t, s, id, agents.AgentView{Name: "checker", Provider: "scripted", Tools: []string{"bureau"}})

	// No toolbox: even though the agent declares a tool, the provider sees no tools
	// and answers directly. No tool calls are made.
	out, err := agents.Invoke(ctx, s, reg, id, "checker", "go")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "completed" || len(out.ToolCalls) != 0 {
		t.Fatalf("expected a plain completion, got %+v", out)
	}
}

func TestInvokeWithToolsRejectsUnknownTool(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	reg := ai.NewRegistry()
	reg.Register(scriptedProvider{})
	defineAgent(t, s, id, agents.AgentView{Name: "checker", Provider: "scripted", Tools: []string{"ghost"}})

	if _, err := agents.InvokeWithTools(ctx, s, reg, &fakeToolbox{}, id, "checker", "go"); err == nil {
		t.Fatal("a declared-but-unknown tool should fail loudly")
	}
}

func TestInvokeWithToolsStepLimit(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	reg := ai.NewRegistry()
	reg.Register(alwaysToolProvider{})
	defineAgent(t, s, id, agents.AgentView{Name: "spin", Provider: "loopy", Tools: []string{"bureau"}})

	out, err := agents.InvokeWithTools(ctx, s, reg, &fakeToolbox{}, id, "spin", "go")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "failed" || !strings.Contains(out.Error, "exceeded") {
		t.Fatalf("expected a step-limit failure, got %+v", out)
	}
}
