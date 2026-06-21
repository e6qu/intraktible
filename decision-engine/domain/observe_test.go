// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"testing"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
)

type recordedNode struct {
	id     string
	typ    events.NodeType
	failed bool
}

// recordingObserver captures the order and outcome of node evaluations.
type recordingObserver struct{ nodes []recordedNode }

func (o *recordingObserver) NodeStart(id string, typ events.NodeType) func(error) {
	idx := len(o.nodes)
	o.nodes = append(o.nodes, recordedNode{id: id, typ: typ})
	return func(err error) {
		if err != nil {
			o.nodes[idx].failed = true
		}
	}
}

// The observer is called once per node, in execution order, with the right id and
// type, and the finish callback never reports an error on a clean run.
func TestExecuteObservedOrder(t *testing.T) {
	assign := cfgNode("m", events.NodeAssignment, `{"assignments":[{"target":"x","expr":"1 + 1"}]}`)
	out := cfgNode("out", events.NodeOutput, `{"fields":["x"]}`)
	obs := &recordingObserver{}
	run := domain.ExecuteObserved(linear(assign, out), map[string]any{}, obs)
	if run.Status != domain.StatusCompleted {
		t.Fatalf("status=%s err=%s", run.Status, run.Err)
	}
	want := []recordedNode{
		{id: "in", typ: events.NodeInput},
		{id: "m", typ: events.NodeAssignment},
		{id: "out", typ: events.NodeOutput},
	}
	if len(obs.nodes) != len(want) {
		t.Fatalf("observed %d nodes, want %d: %+v", len(obs.nodes), len(want), obs.nodes)
	}
	for i, w := range want {
		if obs.nodes[i].id != w.id || obs.nodes[i].typ != w.typ || obs.nodes[i].failed {
			t.Fatalf("node %d = %+v, want %+v", i, obs.nodes[i], w)
		}
	}
}

// A node that errors during evaluation reports the error to its finish callback.
func TestExecuteObservedFailure(t *testing.T) {
	bad := cfgNode("m", events.NodeRule, `{"rules":[{"when":"this is not valid","then":[]}]}`)
	out := cfgNode("out", events.NodeOutput, `{"fields":["x"]}`)
	obs := &recordingObserver{}
	run := domain.ExecuteObserved(linear(bad, out), map[string]any{}, obs)
	if run.Status != domain.StatusFailed {
		t.Fatalf("expected failure, got %s", run.Status)
	}
	last := obs.nodes[len(obs.nodes)-1]
	if last.id != "m" || !last.failed {
		t.Fatalf("expected the rule node to be observed as failed, got %+v", obs.nodes)
	}
}

// A nil observer behaves exactly like plain Execute (no observation, same result).
func TestExecuteObservedNilEqualsExecute(t *testing.T) {
	assign := cfgNode("m", events.NodeAssignment, `{"assignments":[{"target":"x","expr":"2 * 21"}]}`)
	out := cfgNode("out", events.NodeOutput, `{"fields":["x"]}`)
	g := linear(assign, out)
	a := outputJSON(t, domain.Execute(g, map[string]any{}))
	b := outputJSON(t, domain.ExecuteObserved(g, map[string]any{}, nil))
	if a != b {
		t.Fatalf("Execute=%s ExecuteObserved(nil)=%s", a, b)
	}
}
