// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
)

// FuzzResume asserts domain.Resume never panics and always lands a valid terminal
// status for an arbitrary (graph, suspend state, outcome) triple — a malformed or
// cyclic resume target, a Record carrying weird types, an empty graph. Resume runs
// the graph from a saved instance pause point, so it is the durable counterpart of
// Execute and must be just as crash-proof on attacker-influenced persisted state.
func FuzzResume(f *testing.F) {
	seeds := []struct{ graph, state, outcome string }{
		{
			`{"nodes":[{"id":"in","type":"input"},{"id":"m","type":"manual_review","config":{"company_name":"'x'","case_type":"'fraud'","suspend":true,"output_key":"review"}},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"m"},{"from":"m","to":"o"}]}`,
			`{"node_id":"m","resume_node":"o","output_key":"review","record":{"score":5}}`,
			`{"decision":"approve"}`,
		},
		{
			`{"nodes":[{"id":"in","type":"input"},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"o"}]}`,
			`{"node_id":"m","resume_node":"nope","output_key":"","record":{}}`,
			`{}`,
		},
		{
			`{}`,
			`{"node_id":"x","resume_node":"x","record":{"a":[1,2,{"b":true}]}}`,
			`{"k":null}`,
		},
		{
			`{"nodes":[{"id":"a","type":"split","config":{"condition":"review.decision == 'yes'"}}],"edges":[{"from":"a","to":"a","branch":"yes"}]}`,
			`{"node_id":"a","resume_node":"a","output_key":"review","record":{"review":{"x":1}}}`,
			`{"decision":"yes"}`,
		},
	}
	for _, s := range seeds {
		f.Add(s.graph, s.state, s.outcome)
	}
	f.Fuzz(func(t *testing.T, graphJSON, stateJSON, outcomeJSON string) {
		if !json.Valid([]byte(graphJSON)) || !json.Valid([]byte(stateJSON)) || !json.Valid([]byte(outcomeJSON)) {
			return
		}
		var g events.Graph
		if err := json.Unmarshal([]byte(graphJSON), &g); err != nil {
			return
		}
		var state domain.SuspendState
		if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
			return
		}
		var outcome map[string]any
		if err := json.Unmarshal([]byte(outcomeJSON), &outcome); err != nil {
			return
		}

		run := domain.Resume(g, state, outcome)
		// INVARIANT: Resume always returns a valid terminal status, never panics.
		if !run.Status.Valid() {
			t.Fatalf("Resume produced invalid status %q", run.Status)
		}

		// The captured suspend state must marshal→unmarshal stably, since the command
		// path persists it as a DecisionSuspended event and reloads it on resume.
		b, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("marshal SuspendState: %v", err)
		}
		var round domain.SuspendState
		if err := json.Unmarshal(b, &round); err != nil {
			t.Fatalf("unmarshal SuspendState round-trip: %v", err)
		}
		b2, err := json.Marshal(round)
		if err != nil {
			t.Fatalf("re-marshal SuspendState: %v", err)
		}
		if !bytes.Equal(b, b2) {
			t.Fatalf("SuspendState round-trip not stable:\n %s\n %s", b, b2)
		}
		// A re-suspend on resume must carry a non-nil Suspend (the command path
		// dereferences run.Suspend.NodeID), and resuming again from it must stay valid.
		if run.Status == domain.StatusSuspended {
			if run.Suspend == nil {
				t.Fatalf("suspended run has nil Suspend state")
			}
			again := domain.Resume(g, *run.Suspend, outcome)
			if !again.Status.Valid() {
				t.Fatalf("re-Resume produced invalid status %q", again.Status)
			}
		}
	})
}
