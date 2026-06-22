// SPDX-License-Identifier: AGPL-3.0-or-later

package mrm_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/agent-manager/eval"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/mrm"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

func TestBuildInventoryAndIssues(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main"}
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	// A flow with a published version but no validation assertions and no SLO.
	put(t, st, flows.Collection, store.Key("demo", "main", "f1"), flows.FlowView{
		Org: "demo", Workspace: "main", FlowID: "f1", Name: "KYC", Latest: 2,
		Versions:    []flows.VersionView{{Version: 1, PublishedBy: "alice"}, {Version: 2, PublishedBy: "bob"}},
		Deployments: map[string]flows.DeploymentView{"production": {Version: 2}},
	})
	// An agent with no eval cases defined.
	put(t, st, agents.CollectionAgents, store.Key("demo", "main", "triage"), agents.AgentView{
		Org: "demo", Workspace: "main", Name: "triage", Latest: 1, Runs: 7,
		Versions: []agents.AgentVersionView{{Version: 1, PublishedBy: "carol"}},
	})

	rep, err := mrm.Build(ctx, st, id, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Total != 2 || rep.Summary.ByKind[mrm.KindFlow] != 1 || rep.Summary.ByKind[mrm.KindAgent] != 1 {
		t.Fatalf("summary = %+v", rep.Summary)
	}
	// Both lack validation → unvalidated + flagged with issues; the flow is deployed.
	if rep.Summary.Unvalidated != 2 || rep.Summary.WithIssues != 2 || rep.Summary.Deployed != 1 {
		t.Fatalf("summary counts = %+v", rep.Summary)
	}

	byID := map[string]mrm.Model{}
	for _, m := range rep.Models {
		byID[m.ID] = m
	}
	flow := byID["f1"]
	if flow.Owner != "bob" || flow.Version != 2 || flow.Deployments["production"] != 2 {
		t.Fatalf("flow entry = %+v", flow)
	}
	if !hasIssue(flow.Issues, "no validation assertions defined") {
		t.Fatalf("flow should flag missing validation: %v", flow.Issues)
	}
	agent := byID["triage"]
	if agent.Owner != "carol" || agent.Monitoring.Decisions != 7 {
		t.Fatalf("agent entry = %+v", agent)
	}
	if !hasIssue(agent.Issues, "no eval cases defined") {
		t.Fatalf("agent should flag missing eval cases: %v", agent.Issues)
	}
}

func TestValidatedAgentHasNoIssue(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main"}
	put(t, st, agents.CollectionAgents, store.Key("demo", "main", "ok"), agents.AgentView{
		Org: "demo", Workspace: "main", Name: "ok", Latest: 1,
	})
	put(t, st, eval.Collection, store.Key("demo", "main", "ok"), eval.View{
		Org: "demo", Workspace: "main", Agent: "ok", Cases: []eval.Case{{Name: "c1", Prompt: "p"}},
	})
	rep, err := mrm.Build(ctx, st, id, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	m := rep.Models[0]
	if m.Validation.Coverage != mrm.CoverageTested || m.Validation.EvalCases != 1 || len(m.Issues) != 0 {
		t.Fatalf("a validated agent should be tested with no issues: %+v", m)
	}
}

func TestExports(t *testing.T) {
	rep := mrm.Report{
		Org: "demo", Workspace: "main", GeneratedAt: time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		Summary: mrm.Summary{Total: 1, ByKind: map[mrm.Kind]int{mrm.KindFlow: 1}, WithIssues: 1},
		Models: []mrm.Model{{
			Kind: mrm.KindFlow, ID: "f1", Name: "KYC", Version: 2, Owner: "bob",
			Validation: mrm.Validation{Coverage: mrm.CoverageNone},
			Issues:     []string{"no validation assertions defined"},
		}},
	}
	csv, err := mrm.CSV(rep)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(csv, "kind,id,name") || !strings.Contains(csv, "flow,f1,KYC") {
		t.Fatalf("csv = %q", csv)
	}
	md := mrm.Markdown(rep)
	if !strings.Contains(md, "# Model Risk Report") || !strings.Contains(md, "no validation assertions defined") {
		t.Fatalf("markdown = %q", md)
	}
}

// csvSafe defuses spreadsheet formula injection in an id-shaped value.
func TestCSVFormulaInjection(t *testing.T) {
	rep := mrm.Report{Models: []mrm.Model{{Kind: mrm.KindFlow, ID: "=cmd()", Name: "x"}}}
	csv, err := mrm.CSV(rep)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(csv, "'=cmd()") {
		t.Fatalf("formula trigger not neutralized: %q", csv)
	}
}

func put[T any](t *testing.T, s store.Store, collection, key string, v T) {
	t.Helper()
	if err := store.PutDoc(context.Background(), s, collection, key, v); err != nil {
		t.Fatal(err)
	}
}

func hasIssue(issues []string, want string) bool {
	for _, i := range issues {
		if i == want {
			return true
		}
	}
	return false
}
