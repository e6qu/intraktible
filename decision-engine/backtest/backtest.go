// SPDX-License-Identifier: AGPL-3.0-or-later

// Package backtest replays a dataset of inputs through a flow version — and
// optionally compares two versions — using the pure, deterministic execution
// core. It performs NO I/O and records NO production decisions: it is a
// side-effect-free simulation, the confidence tool you run before deploying a
// change. Inputs are the full flow inputs (with any pre-resolved
// features.*/connect.*/ai.* the flow reads), so execution stays pure.
package backtest

import (
	"encoding/json"
	"reflect"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
)

// Outcome is one input's result under one flow version.
type Outcome struct {
	Status string         `json:"status"` // completed | failed
	Output map[string]any `json:"output,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// RecordResult is one dataset record's outcome(s). Candidate/Changed are only set
// in compare mode.
type RecordResult struct {
	Index     int      `json:"index"`
	Baseline  Outcome  `json:"baseline"`
	Candidate *Outcome `json:"candidate,omitempty"`
	Changed   bool     `json:"changed,omitempty"`
}

// Summary aggregates a backtest run.
type Summary struct {
	Total              int  `json:"total"`
	Compare            bool `json:"compare"`
	BaselineCompleted  int  `json:"baseline_completed"`
	BaselineFailed     int  `json:"baseline_failed"`
	CandidateCompleted int  `json:"candidate_completed,omitempty"`
	CandidateFailed    int  `json:"candidate_failed,omitempty"`
	Changed            int  `json:"changed"` // records whose outcome differs between versions
}

// Report is the full result of a backtest.
type Report struct {
	Summary Summary        `json:"summary"`
	Records []RecordResult `json:"records"`
}

// Run replays each input through baseline — and through candidate, when non-nil,
// flagging records whose outcome changed — and returns a report. Each execution
// gets its own deep copy of the input (Execute mutates the input as the working
// context), so records and versions never interfere.
func Run(baseline events.Graph, candidate *events.Graph, inputs []map[string]any) Report {
	rep := Report{Summary: Summary{Total: len(inputs), Compare: candidate != nil}}
	rep.Records = make([]RecordResult, 0, len(inputs))
	for i, in := range inputs {
		base := execute(baseline, in)
		rec := RecordResult{Index: i, Baseline: base}
		countOutcome(base, &rep.Summary.BaselineCompleted, &rep.Summary.BaselineFailed)
		if candidate != nil {
			cand := execute(*candidate, in)
			rec.Candidate = &cand
			countOutcome(cand, &rep.Summary.CandidateCompleted, &rep.Summary.CandidateFailed)
			if !sameOutcome(base, cand) {
				rec.Changed = true
				rep.Summary.Changed++
			}
		}
		rep.Records = append(rep.Records, rec)
	}
	return rep
}

// execute runs one graph against a fresh copy of input and flattens the Run.
func execute(g events.Graph, input map[string]any) Outcome {
	run := domain.Execute(g, cloneInput(input))
	return Outcome{Status: run.Status, Output: run.Output, Error: run.Err}
}

func countOutcome(o Outcome, completed, failed *int) {
	if o.Status == domain.StatusCompleted {
		*completed++
	} else {
		*failed++
	}
}

// sameOutcome reports whether two outcomes are equivalent (same status + output).
func sameOutcome(a, b Outcome) bool {
	return a.Status == b.Status && reflect.DeepEqual(a.Output, b.Output)
}

// cloneInput deep-copies an input map so Execute's in-place mutation of one run
// never leaks into another. A JSON round-trip is the simplest correct deep copy
// for these JSON-decoded values.
func cloneInput(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	b, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]any{}
	}
	return out
}
