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

// tierBranch is the shared tail of the feature/connect fixtures: a split on cond
// into high/low assignments of `tier`, ending at an output of just `tier`. head is
// the node the input connects to and the split's predecessor.
func tierBranch(cond, head string) events.Graph {
	cfg := func(s string) json.RawMessage { return json.RawMessage(s) }
	return events.Graph{
		Nodes: []events.Node{
			{ID: "s", Type: events.NodeSplit, Config: cfg(`{"condition":"` + cond + `"}`)},
			{ID: "high", Type: events.NodeAssignment, Config: cfg(`{"assignments":[{"target":"tier","expr":"'high'"}]}`)},
			{ID: "low", Type: events.NodeAssignment, Config: cfg(`{"assignments":[{"target":"tier","expr":"'low'"}]}`)},
			{ID: "out", Type: events.NodeOutput, Config: cfg(`{"fields":["tier"]}`)},
		},
		Edges: []events.Edge{
			{From: head, To: "s"},
			{From: "s", To: "high", Branch: "yes"},
			{From: "s", To: "low", Branch: "no"},
			{From: "high", To: "out"},
			{From: "low", To: "out"},
		},
	}
}

// ConnectGraph is a flow that calls a connector then branches on its response:
// input -> connect(bureau) -> split(connect.bureau.score >= 50) -> high/low ->
// output(tier). The connector call is pre-resolved by the shell.
func ConnectGraph() events.Graph {
	g := tierBranch("connect.bureau.score >= 50", "c")
	g.Nodes = append([]events.Node{
		{ID: "in", Type: events.NodeInput},
		{ID: "c", Type: events.NodeConnect, Config: json.RawMessage(`{"connector":"bureau","output":"bureau"}`)},
	}, g.Nodes...)
	g.Edges = append([]events.Edge{{From: "in", To: "c"}}, g.Edges...)
	return g
}

// AIGraph is a flow that runs an agent then branches on its structured output:
// input -> ai(assess) -> split(ai.assess.score >= 50) -> high/low -> output(tier).
func AIGraph() events.Graph {
	g := tierBranch("ai.assess.score >= 50", "a")
	g.Nodes = append([]events.Node{
		{ID: "in", Type: events.NodeInput},
		{ID: "a", Type: events.NodeAI, Config: json.RawMessage(`{"agent":"assess","output":"assess"}`)},
	}, g.Nodes...)
	g.Edges = append([]events.Edge{{From: "in", To: "a"}}, g.Edges...)
	return g
}

// PredictGraph is a flow that scores an input with a model then branches on the
// prediction: input -> predict(risk) -> split(predict.risk.probability >= 0.5) ->
// high/low -> output(tier). The model evaluation is pre-resolved by the shell.
func PredictGraph() events.Graph {
	g := tierBranch("predict.risk.probability >= 0.5", "p")
	g.Nodes = append([]events.Node{
		{ID: "in", Type: events.NodeInput},
		{ID: "p", Type: events.NodePredict, Config: json.RawMessage(`{"model":"risk","output":"risk"}`)},
	}, g.Nodes...)
	g.Edges = append([]events.Edge{{From: "in", To: "p"}}, g.Edges...)
	return g
}

// FeatureGraph is a flow whose Split reads an injected Context Layer feature
// (features.txn_count_24h): input -> split(>= 3) -> high/low -> output(tier).
func FeatureGraph() events.Graph {
	g := tierBranch("features.txn_count_24h >= 3", "in")
	g.Nodes = append([]events.Node{{ID: "in", Type: events.NodeInput}}, g.Nodes...)
	return g
}
