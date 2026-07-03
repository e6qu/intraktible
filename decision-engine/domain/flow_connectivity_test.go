// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"strings"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
)

func graphOf(nodes []events.Node, edges []events.Edge) events.Graph {
	return events.Graph{Nodes: nodes, Edges: edges}
}

// The publish dry-compile rejects a graph whose non-output node leads nowhere —
// previously it published fine and the decision dead-ended mid-graph.
func TestValidateGraphRejectsDeadEnd(t *testing.T) {
	g := graphOf(
		[]events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "a", Type: events.NodeAssignment},
			{ID: "out", Type: events.NodeOutput},
		},
		[]events.Edge{{From: "in", To: "a"}, {From: "in", To: "out"}},
	)
	err := domain.ValidateGraph(g)
	if err == nil || !strings.Contains(err.Error(), `"a" dead-ends`) {
		t.Fatalf("want dead-end rejection, got %v", err)
	}
}

func TestValidateGraphRejectsUnreachable(t *testing.T) {
	g := graphOf(
		[]events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "orphan", Type: events.NodeAssignment},
			{ID: "out", Type: events.NodeOutput},
		},
		[]events.Edge{{From: "in", To: "out"}, {From: "orphan", To: "out"}},
	)
	err := domain.ValidateGraph(g)
	if err == nil || !strings.Contains(err.Error(), `"orphan" is unreachable`) {
		t.Fatalf("want unreachable rejection, got %v", err)
	}
}

// Defense in depth below the publish gate: a run that walks into a non-output
// dead end (possible only for graphs that bypassed validation) fails loudly
// instead of recording a quiet "completed" with the raw context as output.
func TestExecuteFailsAtNonOutputDeadEnd(t *testing.T) {
	g := graphOf(
		[]events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "a", Type: events.NodeAssignment},
		},
		[]events.Edge{{From: "in", To: "a"}},
	)
	run := domain.Execute(g, map[string]any{"x": 1})
	if run.Status != domain.StatusFailed || !strings.Contains(run.Err, `dead-ends at non-output node "a"`) {
		t.Fatalf("want loud dead-end failure, got status=%s err=%q", run.Status, run.Err)
	}
}
