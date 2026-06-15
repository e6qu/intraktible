// SPDX-License-Identifier: AGPL-3.0-or-later

// Package flowtest provides shared flow fixtures for decision-engine tests so
// the graph builders are defined once across the unit/integration/e2e layers.
package flowtest

import (
	"encoding/json"
	"fmt"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// FailingGraph is a valid graph whose Assignment references an undefined field,
// so every decision through it fails loudly at node "a".
func FailingGraph() events.Graph {
	return events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "a", Type: events.NodeAssignment, Config: json.RawMessage(`{"assignments":[{"target":"y","expr":"undefined_field + 1"}]}`)},
			{ID: "out", Type: events.NodeOutput},
		},
		Edges: []events.Edge{{From: "in", To: "a"}, {From: "a", To: "out"}},
	}
}

// ConstGraph is a flow that outputs a constant {"decision": <value>}, used to
// tell which version ran in version-routing tests.
func ConstGraph(value string) events.Graph {
	cfg := json.RawMessage(fmt.Sprintf(`{"assignments":[{"target":"decision","expr":"'%s'"}]}`, value))
	return events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "a", Type: events.NodeAssignment, Config: cfg},
			{ID: "out", Type: events.NodeOutput, Config: json.RawMessage(`{"fields":["decision"]}`)},
		},
		Edges: []events.Edge{{From: "in", To: "a"}, {From: "a", To: "out"}},
	}
}

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

// FeatureGraph is a flow whose Split reads an injected Context Layer feature
// (features.txn_count_24h): input -> split(>= 3) -> high/low -> output(tier).
func FeatureGraph() events.Graph {
	cfg := func(s string) json.RawMessage { return json.RawMessage(s) }
	return events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "s", Type: events.NodeSplit, Config: cfg(`{"condition":"features.txn_count_24h >= 3"}`)},
			{ID: "high", Type: events.NodeAssignment, Config: cfg(`{"assignments":[{"target":"tier","expr":"'high'"}]}`)},
			{ID: "low", Type: events.NodeAssignment, Config: cfg(`{"assignments":[{"target":"tier","expr":"'low'"}]}`)},
			{ID: "out", Type: events.NodeOutput, Config: cfg(`{"fields":["tier"]}`)},
		},
		Edges: []events.Edge{
			{From: "in", To: "s"},
			{From: "s", To: "high", Branch: "yes"},
			{From: "s", To: "low", Branch: "no"},
			{From: "high", To: "out"},
			{From: "low", To: "out"},
		},
	}
}
