// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
)

func cfgNode(id string, t events.NodeType, config string) events.Node {
	n := events.Node{ID: id, Type: t}
	if config != "" {
		n.Config = json.RawMessage(config)
	}
	return n
}

// linear builds an input -> mid -> out flow.
func linear(mid, out events.Node) events.Graph {
	return events.Graph{
		Nodes: []events.Node{cfgNode("in", events.NodeInput, ""), mid, out},
		Edges: []events.Edge{{From: "in", To: mid.ID}, {From: mid.ID, To: out.ID}},
	}
}

func outputJSON(t *testing.T, run domain.Run) string {
	t.Helper()
	if run.Status != domain.StatusCompleted {
		t.Fatalf("status=%s err=%s", run.Status, run.Err)
	}
	b, err := json.Marshal(run.Output)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestExecuteLinear(t *testing.T) {
	const (
		ruleCfg = `{"rules":[{"when":"fico < 600","then":[{"target":"tier","expr":"'low'"}]}]}`
		twoCfg  = `{"assignments":[{"target":"x","expr":"a + b"},{"target":"y","expr":"x * 2"}]}`
	)
	rule := cfgNode("m", events.NodeRule, ruleCfg)
	cases := []struct {
		name  string
		graph events.Graph
		input map[string]any
		want  string
	}{
		{
			"assignment",
			linear(cfgNode("m", events.NodeAssignment, `{"assignments":[{"target":"score","expr":"fico + 10"}]}`), cfgNode("out", events.NodeOutput, `{"fields":["score"]}`)),
			map[string]any{"fico": 700}, `{"score":710}`,
		},
		{"rule fires", linear(rule, cfgNode("out", events.NodeOutput, `{"fields":["tier"]}`)), map[string]any{"fico": 550}, `{"tier":"low"}`},
		{"rule skips", linear(rule, cfgNode("out", events.NodeOutput, `{"fields":["tier"]}`)), map[string]any{"fico": 800}, `{"tier":null}`},
		{"chained deterministically", linear(cfgNode("m", events.NodeAssignment, twoCfg), cfgNode("out", events.NodeOutput, "")), map[string]any{"a": 3, "b": 4}, `{"a":3,"b":4,"x":7,"y":14}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := outputJSON(t, domain.Execute(c.graph, c.input))
			if got != c.want {
				t.Fatalf("output=%s, want %s", got, c.want)
			}
			// Same inputs must reproduce the same output (replay prerequisite).
			if again := outputJSON(t, domain.Execute(c.graph, c.input)); again != got {
				t.Fatalf("non-deterministic: %s != %s", again, got)
			}
		})
	}
}

func splitGraph() events.Graph {
	return events.Graph{
		Nodes: []events.Node{
			cfgNode("in", events.NodeInput, ""),
			cfgNode("s", events.NodeSplit, `{"condition":"amount > 1000"}`),
			cfgNode("yes", events.NodeAssignment, `{"assignments":[{"target":"decision","expr":"'APPROVE'"}]}`),
			cfgNode("no", events.NodeAssignment, `{"assignments":[{"target":"decision","expr":"'DECLINE'"}]}`),
			cfgNode("out", events.NodeOutput, `{"fields":["decision"]}`),
		},
		Edges: []events.Edge{
			{From: "in", To: "s"},
			{From: "s", To: "yes", Branch: "yes"},
			{From: "s", To: "no", Branch: "no"},
			{From: "yes", To: "out"},
			{From: "no", To: "out"},
		},
	}
}

func TestExecuteSplit(t *testing.T) {
	g := splitGraph()
	approve := domain.Execute(g, map[string]any{"amount": 5000})
	if got := outputJSON(t, approve); got != `{"decision":"APPROVE"}` {
		t.Fatalf("yes branch: %s", got)
	}
	for _, r := range approve.Results {
		if r.NodeID == "no" {
			t.Fatal("the not-taken branch must not be evaluated")
		}
	}
	if got := outputJSON(t, domain.Execute(g, map[string]any{"amount": 500})); got != `{"decision":"DECLINE"}` {
		t.Fatalf("no branch: %s", got)
	}
}

func TestExecuteFailsLoudly(t *testing.T) {
	cases := []struct {
		name       string
		graph      events.Graph
		failedNode string
	}{
		{
			"bad expression",
			linear(cfgNode("a", events.NodeAssignment, `{"assignments":[{"target":"x","expr":"fico +"}]}`), cfgNode("out", events.NodeOutput, "")),
			"a",
		},
		{
			"unsupported node type",
			linear(cfgNode("ai", events.NodeAI, ""), cfgNode("out", events.NodeOutput, "")),
			"ai",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := domain.Execute(c.graph, map[string]any{"fico": 1})
			if run.Status != domain.StatusFailed || run.FailedNode != c.failedNode || run.Err == "" {
				t.Fatalf("expected loud failure at %q, got %+v", c.failedNode, run)
			}
		})
	}
}
