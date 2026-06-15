// SPDX-License-Identifier: AGPL-3.0-or-later

// Package flowtest provides shared flow fixtures for decision-engine tests so
// the graph builders are defined once across the unit/integration/e2e layers.
package flowtest

import (
	"encoding/json"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// LinearGraph is a minimal valid flow: input -> rule -> output.
func LinearGraph() events.Graph {
	return events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "r", Type: events.NodeRule},
			{ID: "out", Type: events.NodeOutput},
		},
		Edges: []events.Edge{{From: "in", To: "r"}, {From: "r", To: "out"}},
	}
}

// DecisionGraph is a small executable flow used by the decide/history tests:
// input -> assignment(score = fico + bonus) -> split(score >= 700) ->
// approve/decline -> output(decision, score).
func DecisionGraph() events.Graph {
	cfg := func(s string) json.RawMessage { return json.RawMessage(s) }
	return events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "score", Type: events.NodeAssignment, Config: cfg(`{"assignments":[{"target":"score","expr":"fico + bonus"}]}`)},
			{ID: "s", Type: events.NodeSplit, Config: cfg(`{"condition":"score >= 700"}`)},
			{ID: "approve", Type: events.NodeAssignment, Config: cfg(`{"assignments":[{"target":"decision","expr":"'APPROVE'"}]}`)},
			{ID: "decline", Type: events.NodeAssignment, Config: cfg(`{"assignments":[{"target":"decision","expr":"'DECLINE'"}]}`)},
			{ID: "out", Type: events.NodeOutput, Config: cfg(`{"fields":["decision","score"]}`)},
		},
		Edges: []events.Edge{
			{From: "in", To: "score"},
			{From: "score", To: "s"},
			{From: "s", To: "approve", Branch: "yes"},
			{From: "s", To: "decline", Branch: "no"},
			{From: "approve", To: "out"},
			{From: "decline", To: "out"},
		},
	}
}
