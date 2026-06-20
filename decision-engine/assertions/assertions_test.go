// SPDX-License-Identifier: AGPL-3.0-or-later

package assertions_test

import (
	"testing"

	"github.com/e6qu/intraktible/decision-engine/assertions"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
)

func TestRun(t *testing.T) {
	g := flowtest.ConstGraph("approve") // always outputs {"decision": "approve"}
	rep := assertions.Run(g, []assertions.Case{
		{Name: "matches", Input: map[string]any{}, Expect: map[string]any{"decision": "approve"}},
		{Name: "wrong value", Input: map[string]any{}, Expect: map[string]any{"decision": "decline"}},
	})
	if rep.Total != 2 || rep.Passed != 1 || rep.Failed != 1 {
		t.Fatalf("unexpected tally: %+v", rep)
	}
	if !rep.Results[0].Passed {
		t.Fatalf("case 0 should pass: %+v", rep.Results[0])
	}
	if rep.Results[1].Passed || len(rep.Results[1].Mismatch) != 1 || rep.Results[1].Mismatch[0] != "decision" {
		t.Fatalf("case 1 should fail on the decision field: %+v", rep.Results[1])
	}
}

// Expecting a field to be null must FAIL when the output omits the field entirely
// (absent ≠ explicit null) — otherwise a `null` expectation passes vacuously and an
// assertion suite gives false confidence.
func TestRunNullExpectationFailsOnAbsentField(t *testing.T) {
	g := flowtest.ConstGraph("approve") // outputs {"decision":"approve"} — no "missing" key
	rep := assertions.Run(g, []assertions.Case{
		{Name: "absent vs null", Input: map[string]any{}, Expect: map[string]any{"missing": nil}},
	})
	if rep.Passed != 0 || rep.Results[0].Passed || len(rep.Results[0].Mismatch) != 1 {
		t.Fatalf("a null expectation must not pass against an absent field: %+v", rep.Results[0])
	}
}

func TestRunFailingFlowIsNotPassed(t *testing.T) {
	rep := assertions.Run(flowtest.FailingGraph(), []assertions.Case{
		{Name: "x", Input: map[string]any{}, Expect: map[string]any{"y": 1}},
	})
	if rep.Passed != 0 || rep.Results[0].Status == "completed" || rep.Results[0].Error == "" {
		t.Fatalf("a failed execution must not pass and should carry the error: %+v", rep.Results[0])
	}
}
