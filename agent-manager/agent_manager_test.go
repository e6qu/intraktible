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

	res, err := h.RunAgent(ctx, id, "triage", "hello there", 0)
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
	res, err := h.RunAgent(ctx, id, "extract", "pull the fields", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "completed" || string(res.Structured) != "{}" {
		t.Fatalf("structured run: %+v", res)
	}

	// Running an unknown agent is an error, not a recorded run.
	if _, err := h.RunAgent(ctx, id, "ghost", "x", 0); err == nil {
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
	res, err := h.RunAgent(ctx, id, "strict", "assess", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "failed" || res.Error == "" {
		t.Fatalf("schema-violating output should be a failed run, got %+v", res)
	}
}

// TestAgentVersioning proves redefining an agent appends an immutable version
// (and an identical redefine is idempotent — no redundant version), and that a
// run can pin a past version's config.
func TestAgentVersioning(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()
	h := command.NewHandler(log, st, registry())

	// v1, then a changed config (v2), then an identical redefine of v2 (no new version).
	if _, err := h.DefineAgent(ctx, id, domain.DefineAgent{Name: "a", System: "v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DefineAgent(ctx, id, domain.DefineAgent{Name: "a", System: "v2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DefineAgent(ctx, id, domain.DefineAgent{Name: "a", System: "v2"}); err != nil {
		t.Fatal(err)
	}
	if err := projection.New(log, st, agents.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}

	av, ok, err := agents.Read(ctx, st, id, "a")
	if err != nil || !ok {
		t.Fatal("read agent", err)
	}
	if av.Latest != 2 || len(av.Versions) != 2 {
		t.Fatalf("expected 2 versions (identical redefine is idempotent), got latest=%d versions=%d", av.Latest, len(av.Versions))
	}
	if av.System != "v2" {
		t.Fatalf("top-level config should be the latest: %q", av.System)
	}

	// ReadConfig resolves a pinned version vs latest.
	v1, ok, _ := agents.ReadConfig(ctx, st, id, "a", 1)
	if !ok || v1.System != "v1" {
		t.Fatalf("version 1 config = %+v", v1)
	}
	latest, ok, _ := agents.ReadConfig(ctx, st, id, "a", 0)
	if !ok || latest.System != "v2" {
		t.Fatalf("latest config = %+v", latest)
	}
	if _, ok, _ := agents.ReadConfig(ctx, st, id, "a", 99); ok {
		t.Fatal("unknown version should not resolve")
	}

	// A run pinned to v1 uses v1's config (recorded run reflects the pinned version).
	if _, err := h.RunAgent(ctx, id, "a", "hi", 1); err != nil {
		t.Fatal(err)
	}
}
