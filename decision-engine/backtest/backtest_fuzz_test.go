// SPDX-License-Identifier: AGPL-3.0-or-later

package backtest_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/backtest"
	"github.com/e6qu/intraktible/decision-engine/events"
)

// FuzzReplayRecorded asserts the backtest replay over recorded inputs never panics
// and always lands a valid per-record status. It mirrors service.recordedDataset's
// parse-and-skip contract: each recorded decision's captured Data is a JSON blob,
// a malformed or non-object one is SKIPPED rather than crashing the backtest, and
// the surviving objects are replayed (baseline + an optional candidate) through the
// pure engine. The graph itself is arbitrary so a degenerate version is exercised too.
func FuzzReplayRecorded(f *testing.F) {
	seeds := []struct{ graph, records string }{
		{
			`{"nodes":[{"id":"in","type":"input"},{"id":"a","type":"assignment","config":{"assignments":[{"target":"d","expr":"score > 1 ? 'a' : 'b'"}]}},{"id":"o","type":"output","config":{"fields":["d"]}}],"edges":[{"from":"in","to":"a"},{"from":"a","to":"o"}]}`,
			`[{"score":5},{"score":0},"not-an-object",null,123,{"score":"x"}]`,
		},
		{
			`{}`,
			`[{},{"a":[1,2,3]},{"nested":{"deep":{"x":true}}}]`,
		},
		{
			`{"nodes":[{"id":"in","type":"input"},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"o"}]}`,
			`["broken json fragment",{"k":null}]`,
		},
	}
	for _, s := range seeds {
		f.Add(s.graph, s.records)
	}
	f.Fuzz(func(t *testing.T, graphJSON, recordsJSON string) {
		if !json.Valid([]byte(graphJSON)) || !json.Valid([]byte(recordsJSON)) {
			return
		}
		var g events.Graph
		if err := json.Unmarshal([]byte(graphJSON), &g); err != nil {
			return
		}
		// The "recorded" records are an array of arbitrary JSON values: stand-ins for
		// each history Record's captured Data. Build the dataset exactly as
		// recordedDataset does — unmarshal each into a map, skipping any that won't
		// parse as an object — so a malformed Data can never reach the replay.
		var raw []json.RawMessage
		if err := json.Unmarshal([]byte(recordsJSON), &raw); err != nil {
			return
		}
		dataset := make([]map[string]any, 0, len(raw))
		for _, rec := range raw {
			if len(rec) == 0 {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal(rec, &m); err != nil {
				continue
			}
			dataset = append(dataset, m)
		}
		if len(dataset) == 0 {
			return
		}

		// Replay against baseline only, then against a candidate (the same graph) — the
		// compare path also walks sameOutcome over arbitrary outputs.
		for _, cand := range []*events.Graph{nil, &g} {
			rep := backtest.Run(g, cand, dataset)
			if rep.Summary.Total != len(dataset) {
				t.Fatalf("total = %d, want %d", rep.Summary.Total, len(dataset))
			}
			for _, rec := range rep.Records {
				// A replayed record's status is always one the engine can produce — a
				// suspending manual_review graph flattens to "suspended", everything else
				// to completed|failed. An unknown status means the flatten path is broken.
				switch rec.Baseline.Status {
				case "completed", "failed", "suspended":
				default:
					t.Fatalf("record %d baseline status %q is not a known run status", rec.Index, rec.Baseline.Status)
				}
			}
		}
	})
}
