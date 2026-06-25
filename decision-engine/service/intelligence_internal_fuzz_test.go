// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// FuzzBoundFlip exercises the counterfactual binary search over an arbitrary eval
// oracle and arbitrary from/to bounds (including NaN/Inf). The search must always
// return (bounded by its fixed iteration count) and, when it reports a flip, that
// flip must be a value the oracle actually rates "better" — never a spurious point.
func FuzzBoundFlip(f *testing.F) {
	f.Add(0.0, 100.0, uint64(0x8000000000000000), 50)
	f.Add(-1e9, 1e9, uint64(1), 1)
	f.Add(math.NaN(), math.Inf(1), uint64(7), 3)
	f.Add(0.0, 0.0, uint64(0), 0)
	f.Fuzz(func(t *testing.T, from, to float64, flipMask uint64, flipAfter int) {
		// A deterministic oracle: "better" once the search has probed flipAfter times,
		// gated by a bit of the threshold so the response is not trivially monotone.
		calls := 0
		better := func(d policy.Disposition) bool { return d == policy.Approve }
		eval := func(x float64) (policy.Disposition, bool) {
			i := calls
			calls++
			// Adversarial: flip on/off per a bit pattern, so the search can't assume monotonicity.
			bit := (flipMask >> uint(i&63)) & 1
			isBetter := i >= flipAfter && bit == 1
			if isBetter {
				return policy.Approve, true
			}
			return policy.Decline, true
		}
		fp := boundFlip(eval, from, to, better)
		if fp == nil {
			return
		}
		// INVARIANT: a reported flip's disposition must be one the oracle rates better.
		if !better(fp.disp) {
			t.Fatalf("boundFlip reported non-better disposition %q", fp.disp)
		}
	})
}

// FuzzNumericFields and the ordering/relchange helpers must tolerate any decoded
// JSON object, including NaN/Inf numbers and thousands of fields, without panicking.
func FuzzNumericFields(f *testing.F) {
	f.Add(`{"income":5000,"score":42,"features":{"x":1}}`)
	f.Add(`{}`)
	f.Add(`{"a":null,"b":"s","c":1.5,"d":[1,2]}`)
	f.Fuzz(func(t *testing.T, dataJSON string) {
		if !json.Valid([]byte(dataJSON)) {
			return
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
			return
		}
		if data == nil {
			return
		}
		// Inject NaN/Inf/denormal values JSON itself cannot carry, to stress the math.
		data["nanf"] = math.NaN()
		data["posinf"] = math.Inf(1)
		data["neginf"] = math.Inf(-1)
		data["denorm"] = math.SmallestNonzeroFloat64
		data["huge"] = math.MaxFloat64
		fields := numericFields(data)
		for _, name := range fields {
			if _, ok := data[name].(float64); !ok {
				t.Fatalf("numericFields returned non-numeric field %q", name)
			}
			if isReservedNamespace(name) {
				t.Fatalf("numericFields returned reserved namespace %q", name)
			}
			val := data[name].(float64)
			fl := flip{Field: name, From: val, To: val * 2}
			_ = relChange(fl) // must not panic on NaN/Inf
		}
		_ = closestFlip(math.NaN(), &flipPoint{to: math.Inf(1)}, &flipPoint{to: math.NaN()})
	})
}

// FuzzSearchFlip drives the whole per-field counterfactual search through the real
// engine (domain.Execute) over an arbitrary publishable graph and an arbitrary input
// map, including NaN/Inf field values. With no policy bound the derived disposition is
// empty (never "better"), but the search must still terminate within its eval budget
// and never panic; if it ever does report a flip it must be well-formed.
func FuzzSearchFlip(f *testing.F) {
	seeds := []struct{ graph, data string }{
		{`{"nodes":[{"id":"in","type":"input"},{"id":"r","type":"rule","config":{"rules":[{"when":"score > 1","then":[{"target":"y","expr":"score * 2"}]}]}},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"r"},{"from":"r","to":"o"}]}`, `{"score":5,"amount":1000}`},
		{`{"nodes":[{"id":"in","type":"input"},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"o"}]}`, `{"x":0}`},
		{`{"nodes":[{"id":"in","type":"input"},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"o"}]}`, `{}`},
	}
	for _, s := range seeds {
		f.Add(s.graph, s.data)
	}
	f.Fuzz(func(t *testing.T, graphJSON, dataJSON string) {
		if !json.Valid([]byte(graphJSON)) || !json.Valid([]byte(dataJSON)) {
			return
		}
		var g events.Graph
		if err := json.Unmarshal([]byte(graphJSON), &g); err != nil {
			return
		}
		if err := domain.ValidateFlow(g); err != nil {
			return
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(dataJSON), &data); err != nil || data == nil {
			return
		}
		// Pathological numeric inputs JSON cannot encode.
		data["nanf"] = math.NaN()
		data["posinf"] = math.Inf(1)
		data["huge"] = math.MaxFloat64
		data["tiny"] = math.SmallestNonzeroFloat64

		s := &Service{store: store.NewMemory()}
		id, err := identity.New("org", "ws", "actor")
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		searched := 0
		for _, field := range numericFields(data) {
			if searched >= cfMaxEvals {
				break
			}
			budget := cfMaxEvals - searched
			if budget > cfEvalsPerField {
				budget = cfEvalsPerField
			}
			fl, used := s.searchFlip(ctx, id, "missing-slug", g, data, field, policy.Decline, budget)
			// INVARIANT: each field's search stays within its budget.
			if used > budget {
				t.Fatalf("searchFlip used %d > budget %d", used, budget)
			}
			searched += used
			if fl != nil {
				// INVARIANT: a reported flip moves the value (to != from) with a coherent direction.
				if fl.To == fl.From {
					t.Fatalf("flip with to==from for field %q", field)
				}
				if fl.Direction != "increase" && fl.Direction != "decrease" {
					t.Fatalf("flip with invalid direction %q", fl.Direction)
				}
				if (fl.Direction == "increase") != (fl.To > fl.From) {
					t.Fatalf("flip direction %q inconsistent with to=%v from=%v", fl.Direction, fl.To, fl.From)
				}
			}
		}
		// INVARIANT: the whole request stays within the global eval cap.
		if searched > cfMaxEvals {
			t.Fatalf("total searched %d > cap %d", searched, cfMaxEvals)
		}
	})
}

// FuzzAggregateNodeStats aggregates an arbitrary set of history records against an
// arbitrary graph. Records may carry node ids not in the graph and arbitrary
// dispositions. The result's per-node count must never exceed the record total and
// pct must stay within [0,1]; the disposition tally must only ever count the three
// tracked buckets.
func FuzzAggregateNodeStats(f *testing.F) {
	f.Add(`{"nodes":[{"id":"a","type":"input"},{"id":"b","type":"output"}]}`,
		`[{"disposition":"approve","nodes":[{"node_id":"a"},{"node_id":"a"},{"node_id":"b"}]},{"disposition":"refer","nodes":[{"node_id":"a"}]}]`)
	f.Add(`{}`, `[]`)
	f.Add(`{"nodes":[{"id":"x","type":"rule"}]}`, `[{"disposition":"weird","nodes":[{"node_id":"ghost"}]}]`)
	f.Fuzz(func(t *testing.T, graphJSON, recordsJSON string) {
		if !json.Valid([]byte(graphJSON)) || !json.Valid([]byte(recordsJSON)) {
			return
		}
		var g events.Graph
		if err := json.Unmarshal([]byte(graphJSON), &g); err != nil {
			return
		}
		var records []history.Record
		if err := json.Unmarshal([]byte(recordsJSON), &records); err != nil {
			return
		}
		resp := aggregateNodeStats(g, records)
		if resp.Total != len(records) {
			t.Fatalf("total %d != record count %d", resp.Total, len(records))
		}
		for _, n := range resp.Nodes {
			if n.Count < 0 || n.Count > resp.Total {
				t.Fatalf("node %q count %d out of [0,%d]", n.NodeID, n.Count, resp.Total)
			}
			if n.Pct < 0 || n.Pct > 1 {
				t.Fatalf("node %q pct %v out of [0,1]", n.NodeID, n.Pct)
			}
		}
		dispSum := 0
		for k, v := range resp.Dispositions {
			if k != "approve" && k != "decline" && k != "refer" {
				t.Fatalf("unexpected disposition bucket %q", k)
			}
			if v < 0 {
				t.Fatalf("negative disposition count for %q", k)
			}
			dispSum += v
		}
		if dispSum > resp.Total {
			t.Fatalf("disposition sum %d > total %d", dispSum, resp.Total)
		}
	})
}

// FuzzCoverageDiscovery exercises the coverage field/threshold discovery and the
// seeded synthetic-input/run loop over an arbitrary graph (adversarial configs and
// branch labels). Discovery must terminate (no catastrophic regex backtracking),
// the run loop must stay bounded, and synthetic sampling must be reproducible.
func FuzzCoverageDiscovery(f *testing.F) {
	seeds := []string{
		`{"nodes":[{"id":"in","type":"input"},{"id":"s","type":"split","config":{"condition":"income > 5000"}},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"s"},{"from":"s","to":"o","branch":"yes"}]}`,
		`{"nodes":[{"id":"n","type":"rule","config":{"rules":[{"when":"connect.bureau.score >= 700 && amount < 1000000","then":[]}]}}]}`,
		`{}`,
	}
	for _, s := range seeds {
		f.Add(s, 50)
	}
	f.Fuzz(func(t *testing.T, graphJSON string, runs int) {
		if !json.Valid([]byte(graphJSON)) {
			return
		}
		var g events.Graph
		if err := json.Unmarshal([]byte(graphJSON), &g); err != nil {
			return
		}
		fields := discoverNumericFields(g)
		// Discovered fields must be sorted, deduped, and exclude reserved idents.
		for i, name := range fields {
			if reservedIdents[name] {
				t.Fatalf("discovered reserved ident %q", name)
			}
			if i > 0 && fields[i-1] >= name {
				t.Fatalf("fields not strictly sorted: %q then %q", fields[i-1], name)
			}
		}
		// Reproducibility: the same (field, i) always yields the same value.
		for _, name := range fields {
			if sampleValue(name, 3) != sampleValue(name, 3) {
				t.Fatalf("sampleValue not reproducible for %q", name)
			}
			v := sampleValue(name, 7)
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("sampleValue produced non-finite %v", v)
			}
		}
		// Run a small bounded fan; with no policy bound this just exercises the loop.
		if runs < 0 {
			runs = 0
		}
		if runs > 30 {
			runs = 30
		}
		s := &Service{store: store.NewMemory()}
		id, err := identity.New("org", "ws", "actor")
		if err != nil {
			t.Fatal(err)
		}
		rep := s.runCoverage(context.Background(), id, "missing", g, fields, runs)
		if rep.Runs != runs {
			t.Fatalf("rep.Runs %d != runs %d", rep.Runs, runs)
		}
		for _, n := range rep.Nodes {
			if n.Hits < 0 || n.Hits > runs {
				t.Fatalf("node %q hits %d out of [0,%d]", n.NodeID, n.Hits, runs)
			}
		}
	})
}
