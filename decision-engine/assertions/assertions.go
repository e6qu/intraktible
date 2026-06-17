// SPDX-License-Identifier: AGPL-3.0-or-later

// Package assertions is the Decision Engine's flow unit tests: input→expected
// cases stored with a flow and run through the pure execution core (no I/O, no
// recorded decision). A case passes when every field in its Expect map equals the
// corresponding field of the flow's output. Used as a pre-deploy/promote gate.
package assertions

import (
	"reflect"

	"github.com/e6qu/intraktible/decision-engine/backtest"
	"github.com/e6qu/intraktible/decision-engine/events"
)

// Case is one stored test: run Input through the flow and expect each field in
// Expect to equal the corresponding output field (a subset match).
type Case struct {
	Name   string         `json:"name"`
	Input  map[string]any `json:"input"`
	Expect map[string]any `json:"expect"`
}

// Result is one case's outcome.
type Result struct {
	Name     string         `json:"name"`
	Passed   bool           `json:"passed"`
	Status   string         `json:"status"` // completed | failed
	Got      map[string]any `json:"got,omitempty"`
	Mismatch []string       `json:"mismatch,omitempty"` // expected fields that did not match
	Error    string         `json:"error,omitempty"`
}

// Report aggregates an assertions run.
type Report struct {
	Total   int      `json:"total"`
	Passed  int      `json:"passed"`
	Failed  int      `json:"failed"`
	Results []Result `json:"results"`
}

// Run executes each case against the graph (via the side-effect-free backtest
// core) and checks the expected subset against the output.
func Run(g events.Graph, cases []Case) Report {
	rep := Report{Total: len(cases), Results: make([]Result, 0, len(cases))}
	for _, c := range cases {
		out := backtest.Run(g, nil, []map[string]any{c.Input}).Records[0].Baseline
		res := Result{Name: c.Name, Status: out.Status, Got: out.Output, Error: out.Error}
		if out.Status == "completed" {
			res.Mismatch = mismatches(c.Expect, out.Output)
			res.Passed = len(res.Mismatch) == 0
		}
		if res.Passed {
			rep.Passed++
		} else {
			rep.Failed++
		}
		rep.Results = append(rep.Results, res)
	}
	return rep
}

// mismatches returns the expected fields whose value differs from the output.
func mismatches(expect, got map[string]any) []string {
	var miss []string
	for k, want := range expect {
		if !reflect.DeepEqual(got[k], want) {
			miss = append(miss, k)
		}
	}
	return miss
}
