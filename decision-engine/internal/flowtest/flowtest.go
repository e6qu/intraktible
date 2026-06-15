// SPDX-License-Identifier: AGPL-3.0-or-later

// Package flowtest provides shared flow fixtures for decision-engine tests so
// the graph builders are defined once across the unit/integration/e2e layers.
package flowtest

import "github.com/e6qu/intraktible/decision-engine/events"

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
