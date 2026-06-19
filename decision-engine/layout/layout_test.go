// SPDX-License-Identifier: AGPL-3.0-or-later

package layout_test

import (
	"testing"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/layout"
)

func node(id, lane string) events.Node { return events.Node{ID: id, Type: "rule", Lane: lane} }

// A linear flow lays out left-to-right by depth; lanes stack vertically in
// first-seen order.
func TestApplyFillsPositionsByDepthAndLane(t *testing.T) {
	g := events.Graph{
		Nodes: []events.Node{
			node("in", "Intake"),
			node("score", "Scoring"),
			node("route", "Decision"),
			node("out", "Decision"),
		},
		Edges: []events.Edge{
			{From: "in", To: "score"},
			{From: "score", To: "route"},
			{From: "route", To: "out"},
		},
	}
	got := layout.Apply(g)
	pos := map[string]events.NodePosition{}
	for _, n := range got.Nodes {
		if n.Position == nil {
			t.Fatalf("node %q has no position", n.ID)
		}
		pos[n.ID] = *n.Position
	}
	// Columns advance by longest-path depth (220 px each).
	if pos["in"].X != 0 || pos["score"].X != 220 || pos["route"].X != 440 || pos["out"].X != 660 {
		t.Fatalf("column X mismatch: %+v", pos)
	}
	// "Decision" is the third lane seen (Intake, Scoring, Decision), so its nodes
	// sit below the first two lanes — Y strictly greater than the Scoring node's.
	if pos["route"].Y <= pos["score"].Y {
		t.Fatalf("lane stacking: route Y %v should be below score Y %v", pos["route"].Y, pos["score"].Y)
	}
	// Deterministic: a second run yields identical positions.
	again := layout.Apply(g)
	for i, n := range again.Nodes {
		if *n.Position != *got.Nodes[i].Position {
			t.Fatalf("layout is not deterministic for %q", n.ID)
		}
	}
}

// A graph that already carries any position is returned untouched (custom layout
// is preserved).
func TestApplyPreservesSuppliedPositions(t *testing.T) {
	custom := &events.NodePosition{X: 7, Y: 13}
	g := events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: "input", Position: custom},
			{ID: "out", Type: "output"},
		},
		Edges: []events.Edge{{From: "in", To: "out"}},
	}
	got := layout.Apply(g)
	if got.Nodes[0].Position == nil || *got.Nodes[0].Position != *custom {
		t.Fatalf("supplied position not preserved: %+v", got.Nodes[0].Position)
	}
	// The position-less node is left as-is too (all-or-nothing).
	if got.Nodes[1].Position != nil {
		t.Fatalf("expected no auto-layout when a custom position exists: %+v", got.Nodes[1].Position)
	}
}
