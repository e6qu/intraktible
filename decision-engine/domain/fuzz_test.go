// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
)

// FuzzValidateFlow asserts publish-time validation never panics on an arbitrary
// graph — it decodes every node's config and dry-compiles expr-lang + Starlark, so
// a malformed config/expression/script must return an error, never crash.
func FuzzValidateFlow(f *testing.F) {
	seeds := []string{
		`{"nodes":[{"id":"in","type":"input"},{"id":"r","type":"rule","config":{"rules":[{"when":"x>1","then":[{"target":"y","expr":"1"}]}]}},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"r"},{"from":"r","to":"o"}]}`,
		`{"nodes":[{"id":"in","type":"input"},{"id":"c","type":"code","config":{"code":"y = 1"}},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"c"},{"from":"c","to":"o"}]}`,
		`{"nodes":[{"id":"in","type":"input"},{"id":"s","type":"split","config":{"condition":"a &&"}},{"id":"o","type":"output"}]}`,
		`{}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, graphJSON string) {
		if !json.Valid([]byte(graphJSON)) {
			return
		}
		var g events.Graph
		if err := json.Unmarshal([]byte(graphJSON), &g); err != nil {
			return
		}
		_ = domain.ValidateFlow(g) // must return, never panic
	})
}

// FuzzExecute asserts the runtime decision path never panics on an arbitrary
// (graph, input) pair and always lands a valid terminal status. ValidateFlow is
// already fuzzed but only dry-compiles; Execute actually evaluates every node
// (scorecard/reason/decision-table/matrix/code) against attacker-influenced input.
// Only graphs that pass ValidateFlow are executed, isolating input-driven bugs from
// graph-shape ones the validator already rejects.
func FuzzExecute(f *testing.F) {
	seeds := []struct{ graph, input string }{
		{`{"nodes":[{"id":"in","type":"input"},{"id":"r","type":"rule","config":{"rules":[{"when":"score > 1","then":[{"target":"y","expr":"score * 2"}]}]}},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"r"},{"from":"r","to":"o"}]}`, `{"score":5}`},
		{`{"nodes":[{"id":"in","type":"input"},{"id":"c","type":"code","config":{"code":"y = input.get('n', 0) + 1"}},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"c"},{"from":"c","to":"o"}]}`, `{"n":3}`},
		{`{"nodes":[{"id":"in","type":"input"},{"id":"d","type":"decision_table","config":{"hit":"collect","aggregate":"sum","rows":[{"when":"a > 0","outputs":[{"target":"t","expr":"a"}]},{"when":"b > 0","outputs":[{"target":"t","expr":"b"}]}]}},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"d"},{"from":"d","to":"o"}]}`, `{"a":2,"b":3}`},
	}
	for _, s := range seeds {
		f.Add(s.graph, s.input)
	}
	f.Fuzz(func(t *testing.T, graphJSON, inputJSON string) {
		if !json.Valid([]byte(graphJSON)) || !json.Valid([]byte(inputJSON)) {
			return
		}
		var g events.Graph
		if err := json.Unmarshal([]byte(graphJSON), &g); err != nil {
			return
		}
		// Only run flows the validator accepts — Execute's contract is over
		// publishable graphs; the validator is fuzzed separately for the rest.
		if err := domain.ValidateFlow(g); err != nil {
			return
		}
		var input map[string]any
		if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
			return
		}
		run := domain.Execute(g, input)
		// INVARIANT: Execute always returns a valid terminal status, never panics.
		if !run.Status.Valid() {
			t.Fatalf("Execute produced invalid status %q", run.Status)
		}
	})
}
