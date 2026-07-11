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

// splitFlow wires a split's branch edges to an output each. branches picks which
// of "yes"/"no" are wired, so a test can try to publish a split missing one; only
// the wired branches get an output node, keeping the graph otherwise connected and
// fully reachable so the branch check is what fails.
func splitFlow(cfg string, branches ...string) events.Graph {
	g := events.Graph{
		Nodes: []events.Node{node("in", events.NodeInput), cfgNode("s", events.NodeSplit, cfg)},
		Edges: []events.Edge{{From: "in", To: "s"}},
	}
	for _, branch := range branches {
		g.Nodes = append(g.Nodes, node(branch+"_out", events.NodeOutput))
		g.Edges = append(g.Edges, events.Edge{From: "s", To: branch + "_out", Branch: branch})
	}
	return g
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
		{"valid split", splitFlow(`{"condition":"input.score >= 700"}`, "yes", "no"), false},
		{"valid code", flow(cfgNode("c", events.NodeCode, `{"code":"x = 1\ny = x + 2"}`)), false},
		{"empty config ok", flow(cfgNode("r", events.NodeRule, ``)), false},

		// A split routes on a boolean, so publishing one with a branch unwired would
		// deploy a graph that fails at decide time on the first input taking it.
		{"split missing the no branch", splitFlow(`{"condition":"input.score >= 700"}`, "yes"), true},
		{"split missing the yes branch", splitFlow(`{"condition":"input.score >= 700"}`, "no"), true},
		{"split with unlabelled edges", flow(cfgNode("s", events.NodeSplit, `{"condition":"input.score >= 700"}`)), true},

		// Structural graph failures still caught.
		{"structurally invalid", events.Graph{}, true},

		// New: semantic per-node failures rejected at publish.
		{"bad rule expr", flow(cfgNode("r", events.NodeRule,
			`{"rules":[{"when":"input.amount >","then":[]}]}`)), true},
		{"bad split expr", splitFlow(`{"condition":"&& nope"}`, "yes", "no"), true},
		{"bad assignment expr", flow(cfgNode("a", events.NodeAssignment,
			`{"assignments":[{"target":"x","expr":"1 +"}]}`)), true},
		{"malformed config shape", splitFlow(`{"condition":123}`, "yes", "no"), true},
		{"unknown config field", splitFlow(`{"conditionx":"true"}`, "yes", "no"), true},
		{"bad starlark", flow(cfgNode("c", events.NodeCode, `{"code":"def ("}`)), true},
		// Hit policy / aggregate are validated at publish, not first decision.
		{"valid decision table any", flow(cfgNode("d", events.NodeDecisionTable,
			`{"hit":"any","rows":[{"when":"true","outputs":[{"target":"x","expr":"1"}]}]}`)), false},
		{"unknown hit policy", flow(cfgNode("d", events.NodeDecisionTable,
			`{"hit":"anyy","rows":[{"when":"true","outputs":[{"target":"x","expr":"1"}]}]}`)), true},
		{"unknown collect aggregate", flow(cfgNode("d", events.NodeDecisionTable,
			`{"hit":"collect","aggregate":"avg","rows":[{"when":"true","outputs":[{"target":"x","expr":"1"}]}]}`)), true},
		// manual_review SLA must be non-negative.
		{"valid manual review", flow(cfgNode("m", events.NodeManualReview,
			`{"company_name":"'Acme'","case_type":"'aml'","sla_days":5}`)), false},
		{"negative sla_days", flow(cfgNode("m", events.NodeManualReview,
			`{"company_name":"'Acme'","case_type":"'aml'","sla_days":-5}`)), true},
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

// TestValidateRejectsDuplicateOutputs guards against two same-namespace nodes
// writing the same output, which would silently discard one result (the connector/
// model is still called) while the other wins by declaration order.
func TestValidateRejectsDuplicateOutputs(t *testing.T) {
	dup := events.Graph{
		Nodes: []events.Node{
			node("in", events.NodeInput),
			cfgNode("c1", events.NodeConnect, `{"connector":"bureau","output":"score"}`),
			cfgNode("c2", events.NodeConnect, `{"connector":"fraud","output":"score"}`),
			node("out", events.NodeOutput),
		},
		Edges: []events.Edge{{From: "in", To: "c1"}, {From: "c1", To: "c2"}, {From: "c2", To: "out"}},
	}
	if err := domain.ValidateFlow(dup); err == nil {
		t.Fatal("expected a duplicate-output rejection for two connect nodes writing \"score\"")
	}
	// Distinct outputs are fine.
	ok := events.Graph{
		Nodes: []events.Node{
			node("in", events.NodeInput),
			cfgNode("c1", events.NodeConnect, `{"connector":"bureau","output":"bureau_score"}`),
			cfgNode("c2", events.NodeConnect, `{"connector":"fraud","output":"fraud_score"}`),
			node("out", events.NodeOutput),
		},
		Edges: []events.Edge{{From: "in", To: "c1"}, {From: "c1", To: "c2"}, {From: "c2", To: "out"}},
	}
	if err := domain.ValidateFlow(ok); err != nil {
		t.Fatalf("distinct connect outputs should validate: %v", err)
	}
}
