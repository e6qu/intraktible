// SPDX-License-Identifier: AGPL-3.0-or-later

package eval_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/agent-manager/command"
	"github.com/e6qu/intraktible/agent-manager/domain"
	"github.com/e6qu/intraktible/agent-manager/eval"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

// harness returns the command handler + a `live` func that starts the projection
// (folding everything seeded so far synchronously, so reads are deterministic).
func harness(t *testing.T) (context.Context, *command.Handler, store.Store, identity.Identity, func()) {
	t.Helper()
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	st := store.NewMemory()
	reg := ai.NewRegistry()
	reg.Register(ai.Stub{})
	h := command.NewHandler(log, st, reg)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	live := func() {
		if err := projection.New(log, st, agents.Projector{}, eval.Projector{}).Start(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, h, st, id, live
}

// The Stub echoes "stub: "+prompt, so contains/equals cases score deterministically.
func TestEvalRunScoring(t *testing.T) {
	ctx, h, _, id, live := harness(t)
	if _, err := h.DefineAgent(ctx, id, domain.DefineAgent{Name: "triage", System: "be terse"}); err != nil {
		t.Fatal(err)
	}
	cases := []eval.Case{
		{Name: "contains-pass", Prompt: "approve this", Mode: eval.ModeContains, Expect: "approve"},
		{Name: "contains-fail", Prompt: "hello", Mode: eval.ModeContains, Expect: "definitely-not-there"},
		{Name: "equals-pass", Prompt: "x", Mode: eval.ModeEquals, Expect: "stub: x"},
	}
	if _, err := h.SetEvalCases(ctx, id, "triage", cases); err != nil {
		t.Fatal(err)
	}
	live() // fold the define + eval-set events

	rep, err := h.RunEvals(ctx, id, "triage", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total != 3 || rep.Passed != 2 || rep.Failed != 1 {
		t.Fatalf("report = %+v", rep)
	}
	byName := map[string]eval.Result{}
	for _, r := range rep.Results {
		byName[r.Name] = r
	}
	if !byName["contains-pass"].Passed || byName["contains-fail"].Passed || !byName["equals-pass"].Passed {
		t.Fatalf("unexpected per-case results: %+v", rep.Results)
	}
	if byName["contains-fail"].Detail == "" {
		t.Fatal("a failed case should carry a detail")
	}
}

// A structured agent (Stub returns {} for a schema request) fails a json_subset case
// that expects a field — exercising the structured scorer.
func TestEvalJSONSubset(t *testing.T) {
	ctx, h, _, id, live := harness(t)
	if _, err := h.DefineAgent(ctx, id, domain.DefineAgent{
		Name: "extract", Schema: json.RawMessage(`{"type":"object"}`),
	}); err != nil {
		t.Fatal(err)
	}
	cases := []eval.Case{
		{Name: "needs-risk", Prompt: "assess", Mode: eval.ModeJSONSubset, ExpectJSON: json.RawMessage(`{"risk":"high"}`)},
		{Name: "empty-ok", Prompt: "assess", Mode: eval.ModeJSONSubset, ExpectJSON: json.RawMessage(`{}`)},
	}
	if _, err := h.SetEvalCases(ctx, id, "extract", cases); err != nil {
		t.Fatal(err)
	}
	live()
	rep, err := h.RunEvals(ctx, id, "extract", 0)
	if err != nil {
		t.Fatal(err)
	}
	// The Stub returns {}: the empty expectation passes, the risk expectation fails.
	if rep.Passed != 1 || rep.Failed != 1 {
		t.Fatalf("json_subset report = %+v", rep)
	}
}

func TestSetCasesValidation(t *testing.T) {
	ctx, h, _, id, _ := harness(t)
	if _, err := h.SetEvalCases(ctx, id, "a", []eval.Case{{Prompt: "x"}}); err == nil {
		t.Fatal("a case with no name should be rejected")
	}
	if _, err := h.SetEvalCases(ctx, id, "a", []eval.Case{{Name: "c", Mode: "bogus"}}); err == nil {
		t.Fatal("an invalid mode should be rejected")
	}
}
