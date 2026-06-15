// SPDX-License-Identifier: AGPL-3.0-or-later

package backtest_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/backtest"
	"github.com/e6qu/intraktible/decision-engine/events"
)

// graph builds input → assignment(decision = expr) → output(decision).
func graph(expr string) events.Graph {
	return events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "a", Type: events.NodeAssignment, Config: json.RawMessage(
				`{"assignments":[{"target":"decision","expr":` + strconv.Quote(expr) + `}]}`)},
			{ID: "out", Type: events.NodeOutput, Config: json.RawMessage(`{"fields":["decision"]}`)},
		},
		Edges: []events.Edge{{From: "in", To: "a"}, {From: "a", To: "out"}},
	}
}

func TestRunSingleVersion(t *testing.T) {
	rep := backtest.Run(graph("'A'"), nil, []map[string]any{{}, {}, {}})
	if rep.Summary.Total != 3 || rep.Summary.BaselineCompleted != 3 || rep.Summary.Compare {
		t.Fatalf("summary = %+v", rep.Summary)
	}
	if len(rep.Records) != 3 || rep.Records[0].Baseline.Output["decision"] != "A" {
		t.Fatalf("records = %+v", rep.Records)
	}
}

func TestRunComparesVersionsAndFlagsChanges(t *testing.T) {
	baseline := graph("'A'")                  // always A
	candidate := graph(`score > 5 ? "A":"B"`) // A or B depending on input
	inputs := []map[string]any{
		{"score": 10.0}, // baseline A, candidate A  -> unchanged
		{"score": 1.0},  // baseline A, candidate B  -> changed
	}
	rep := backtest.Run(baseline, &candidate, inputs)
	if !rep.Summary.Compare || rep.Summary.Total != 2 || rep.Summary.Changed != 1 {
		t.Fatalf("summary = %+v", rep.Summary)
	}
	if rep.Records[0].Changed {
		t.Fatalf("record 0 should be unchanged: %+v", rep.Records[0])
	}
	if !rep.Records[1].Changed || rep.Records[1].Candidate.Output["decision"] != "B" {
		t.Fatalf("record 1 should change to B: %+v", rep.Records[1])
	}
}

func TestRunCountsFailures(t *testing.T) {
	// `score` is undefined for the empty input, so the expression fails loudly.
	g := graph(`score > 5 ? "A":"B"`)
	rep := backtest.Run(g, nil, []map[string]any{{"score": 9.0}, {}})
	if rep.Summary.BaselineCompleted != 1 || rep.Summary.BaselineFailed != 1 {
		t.Fatalf("summary = %+v", rep.Summary)
	}
	if rep.Records[1].Baseline.Status != "failed" || rep.Records[1].Baseline.Error == "" {
		t.Fatalf("record 1 should be a recorded failure: %+v", rep.Records[1])
	}
}

func TestRunIsolatesRecords(t *testing.T) {
	// A run that assigns into the context must not leak into the next record's
	// input (each gets a fresh deep copy).
	g := graph("'X'")
	inputs := []map[string]any{{"a": 1.0}, {"b": 2.0}}
	_ = backtest.Run(g, nil, inputs)
	if _, leaked := inputs[1]["decision"]; leaked {
		t.Fatal("execution mutated the caller's input")
	}
}
