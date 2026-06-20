// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"testing"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
)

// flow wraps a single middle node between input and output with linear edges.
// (cfgNode is shared from execute_test.go.)
func flow(mid events.Node) events.Graph {
	return events.Graph{
		Nodes: []events.Node{node("in", events.NodeInput), mid, node("out", events.NodeOutput)},
		Edges: []events.Edge{{From: "in", To: mid.ID}, {From: mid.ID, To: "out"}},
	}
}

func TestEtagIsCanonical(t *testing.T) {
	a := flow(cfgNode("s", events.NodeSplit, `{"condition":"input.score >= 700"}`))
	// Same meaning, different byte formatting (whitespace + a second key reordered).
	b := flow(cfgNode("s", events.NodeSplit, "{  \"condition\" :  \"input.score >= 700\"  }"))
	ea, err := domain.Etag(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	eb, err := domain.Etag(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ea != eb {
		t.Fatalf("etag should ignore config formatting: %s != %s", ea, eb)
	}
	// A genuine content change yields a different etag.
	c := flow(cfgNode("s", events.NodeSplit, `{"condition":"input.score >= 600"}`))
	ec, _ := domain.Etag(c, nil)
	if ec == ea {
		t.Fatal("a different condition must change the etag")
	}
}

func TestValidateFlow(t *testing.T) {
	cases := []struct {
		name    string
		graph   events.Graph
		wantErr bool
	}{
		{"valid rule", flow(cfgNode("r", events.NodeRule,
			`{"rules":[{"when":"input.amount > 100","then":[{"target":"flag","expr":"true"}]}]}`)), false},
		{"valid split", flow(cfgNode("s", events.NodeSplit, `{"condition":"input.score >= 700"}`)), false},
		{"valid code", flow(cfgNode("c", events.NodeCode, `{"code":"x = 1\ny = x + 2"}`)), false},
		{"empty config ok", flow(cfgNode("r", events.NodeRule, ``)), false},

		// Structural graph failures still caught.
		{"structurally invalid", events.Graph{}, true},

		// New: semantic per-node failures rejected at publish.
		{"bad rule expr", flow(cfgNode("r", events.NodeRule,
			`{"rules":[{"when":"input.amount >","then":[]}]}`)), true},
		{"bad split expr", flow(cfgNode("s", events.NodeSplit, `{"condition":"&& nope"}`)), true},
		{"bad assignment expr", flow(cfgNode("a", events.NodeAssignment,
			`{"assignments":[{"target":"x","expr":"1 +"}]}`)), true},
		{"malformed config shape", flow(cfgNode("s", events.NodeSplit, `{"condition":123}`)), true},
		{"unknown config field", flow(cfgNode("s", events.NodeSplit, `{"conditionx":"true"}`)), true},
		{"bad starlark", flow(cfgNode("c", events.NodeCode, `{"code":"def ("}`)), true},
		// Hit policy / aggregate are validated at publish, not first decision.
		{"valid decision table any", flow(cfgNode("d", events.NodeDecisionTable,
			`{"hit":"any","rows":[{"when":"true","outputs":[{"target":"x","expr":"1"}]}]}`)), false},
		{"unknown hit policy", flow(cfgNode("d", events.NodeDecisionTable,
			`{"hit":"anyy","rows":[{"when":"true","outputs":[{"target":"x","expr":"1"}]}]}`)), true},
		{"unknown collect aggregate", flow(cfgNode("d", events.NodeDecisionTable,
			`{"hit":"collect","aggregate":"avg","rows":[{"when":"true","outputs":[{"target":"x","expr":"1"}]}]}`)), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := domain.ValidateFlow(c.graph)
			if c.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
