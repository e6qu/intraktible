// SPDX-License-Identifier: AGPL-3.0-or-later

package agentmanager_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/agent-manager/command"
	"github.com/e6qu/intraktible/agent-manager/domain"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func registry() *ai.Registry {
	r := ai.NewRegistry()
	r.Register(ai.Stub{})
	return r
}

// TestAgentDefineAndRunReplay defines an agent, runs it (text + structured), then
// rebuilds the read model from the log to prove it is a pure fold of the stream.
func TestAgentDefineAndRunReplay(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()

	h := command.NewHandler(log, st, registry())
	if _, err := h.DefineAgent(ctx, id, domain.DefineAgent{Name: "triage", System: "be terse"}); err != nil {
		t.Fatal(err)
	}
	// The run path reads the agent definition from the read model, so it must be live.
	if err := projection.New(log, st, agents.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}

	res, err := h.RunAgent(ctx, id, "triage", "hello there")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "completed" || res.Text != "stub: hello there" {
		t.Fatalf("run result: %+v", res)
	}

	// Rebuild a fresh read model purely from the log.
	rebuilt := store.NewMemory()
	if err := projection.New(log, rebuilt, agents.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	a, ok, err := agents.Read(ctx, rebuilt, id, "triage")
	if err != nil || !ok {
		t.Fatalf("agent after replay: ok=%v err=%v", ok, err)
	}
	if a.System != "be terse" || a.Runs != 1 {
		t.Fatalf("agent view: %+v", a)
	}
	run, ok, err := agents.GetRun(ctx, rebuilt, id, res.RunID)
	if err != nil || !ok {
		t.Fatalf("run after replay: ok=%v err=%v", ok, err)
	}
	if run.Agent != "triage" || run.Text != "stub: hello there" {
		t.Fatalf("run view: %+v", run)
	}
}

func TestRunAgentStructuredAndUnknown(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()

	h := command.NewHandler(log, st, registry())
	if _, err := h.DefineAgent(ctx, id, domain.DefineAgent{Name: "extract", Schema: json.RawMessage(`{"type":"object"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := projection.New(log, st, agents.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}

	// A schema-constrained agent gets the structured path from the stub.
	res, err := h.RunAgent(ctx, id, "extract", "pull the fields")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "completed" || string(res.Structured) != "{}" {
		t.Fatalf("structured run: %+v", res)
	}

	// Running an unknown agent is an error, not a recorded run.
	if _, err := h.RunAgent(ctx, id, "ghost", "x"); err == nil {
		t.Fatal("expected error running an unknown agent")
	}
}

func TestRunAgentStructuredOutputValidatedAgainstSchema(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()

	h := command.NewHandler(log, st, registry())
	// The agent requires a "risk" field, but the Stub returns {} for schema
	// requests — so the run must be recorded as failed (output violates schema).
	if _, err := h.DefineAgent(ctx, id, domain.DefineAgent{
		Name: "strict", Schema: json.RawMessage(`{"type":"object","required":["risk"]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := projection.New(log, st, agents.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := h.RunAgent(ctx, id, "strict", "assess")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "failed" || res.Error == "" {
		t.Fatalf("schema-violating output should be a failed run, got %+v", res)
	}
}
