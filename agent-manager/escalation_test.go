// SPDX-License-Identifier: AGPL-3.0-or-later

package agentmanager_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/agent-manager/command"
	"github.com/e6qu/intraktible/agent-manager/domain"
	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

// TestEscalateRunOpensCase proves the human-in-the-loop hook: escalating an agent
// run emits the Case Manager's ReviewRequested event, which the cases projector
// (no agent-manager import) consumes to open a case linked back to the run.
func TestEscalateRunOpensCase(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()

	h := command.NewHandler(log, st, registry())
	if _, err := h.DefineAgent(ctx, id, domain.DefineAgent{Name: "triage"}); err != nil {
		t.Fatal(err)
	}
	if err := projection.New(log, st, agents.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := h.RunAgent(ctx, id, "triage", "look at this", 0)
	if err != nil {
		t.Fatal(err)
	}

	caseID, _, err := h.EscalateRun(ctx, id, domain.EscalateRun{
		RunID: run.RunID, CompanyName: "Acme Corp", CaseType: "aml", SLADays: 3,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The Case Manager's projector — which does NOT import agent-manager — opens
	// the case purely from the event stream.
	caseStore := store.NewMemory()
	if err := projection.New(log, caseStore, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	c, ok, err := cases.Read(ctx, caseStore, id, caseID)
	if err != nil || !ok {
		t.Fatalf("case read: ok=%v err=%v", ok, err)
	}
	if c.CompanyName != "Acme Corp" || c.CaseType != "aml" {
		t.Fatalf("case: %+v", c)
	}
	// The case context links back to the agent run.
	var srcCtx map[string]string
	if err := json.Unmarshal(c.Context, &srcCtx); err != nil {
		t.Fatal(err)
	}
	if srcCtx["source"] != "agent" || srcCtx["run_id"] != run.RunID || srcCtx["agent"] != "triage" {
		t.Fatalf("escalation context not linked to the run: %v", srcCtx)
	}
}

// TestEscalateRunDefaultsCaseType proves an escalation with no explicit type opens a
// dedicated agent_review case (per the journeys doc), so agent escalations are
// filterable apart from flow manual-reviews.
func TestEscalateRunDefaultsCaseType(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()
	h := command.NewHandler(log, st, registry())
	if _, err := h.DefineAgent(ctx, id, domain.DefineAgent{Name: "triage"}); err != nil {
		t.Fatal(err)
	}
	if err := projection.New(log, st, agents.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := h.RunAgent(ctx, id, "triage", "look at this", 0)
	if err != nil {
		t.Fatal(err)
	}
	caseID, _, err := h.EscalateRun(ctx, id, domain.EscalateRun{
		RunID: run.RunID, CompanyName: "Acme Corp", SLADays: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	caseStore := store.NewMemory()
	if err := projection.New(log, caseStore, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	c, ok, err := cases.Read(ctx, caseStore, id, caseID)
	if err != nil || !ok {
		t.Fatalf("case read: ok=%v err=%v", ok, err)
	}
	if c.CaseType != "agent_review" {
		t.Fatalf("default case type = %q, want agent_review", c.CaseType)
	}
}

// TestEscalateRunUnknownRun proves a run that exists in neither the projection nor
// the log is rejected (the projection-miss falls through to the scoped log fold,
// which also misses).
func TestEscalateRunUnknownRun(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()
	h := command.NewHandler(log, st, registry())

	if _, _, err := h.EscalateRun(ctx, id, domain.EscalateRun{
		RunID: "does-not-exist", CompanyName: "Acme", CaseType: "aml", SLADays: 3,
	}); err == nil {
		t.Fatal("escalating an unknown run should fail")
	}
}

func TestSummarizeRuns(t *testing.T) {
	runs := []agents.RunView{
		{Agent: "triage", Status: "completed"},
		{Agent: "triage", Status: "failed"},
		{Agent: "extract", Status: "completed"},
	}
	s := agents.SummarizeRuns(runs)
	if s.Total != 3 || s.Completed != 2 || s.Failed != 1 {
		t.Fatalf("summary counts: %+v", s)
	}
	if s.ByAgent["triage"] != 2 || s.ByAgent["extract"] != 1 {
		t.Fatalf("by agent: %+v", s.ByAgent)
	}
}
