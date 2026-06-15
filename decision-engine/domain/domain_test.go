// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"testing"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
)

func node(id string, t events.NodeType) events.Node { return events.Node{ID: id, Type: t} }

func TestValidateGraph(t *testing.T) {
	valid := events.Graph{
		Nodes: []events.Node{node("in", events.NodeInput), node("r", events.NodeRule), node("out", events.NodeOutput)},
		Edges: []events.Edge{{From: "in", To: "r"}, {From: "r", To: "out"}},
	}
	cases := []struct {
		name    string
		graph   events.Graph
		wantErr bool
	}{
		{"valid linear", valid, false},
		{"empty", events.Graph{}, true},
		{"duplicate id", events.Graph{
			Nodes: []events.Node{node("in", events.NodeInput), node("in", events.NodeOutput)},
		}, true},
		{"unknown type", events.Graph{
			Nodes: []events.Node{node("in", events.NodeInput), node("x", "bogus"), node("out", events.NodeOutput)},
		}, true},
		{"no input", events.Graph{
			Nodes: []events.Node{node("r", events.NodeRule), node("out", events.NodeOutput)},
			Edges: []events.Edge{{From: "r", To: "out"}},
		}, true},
		{"two inputs", events.Graph{
			Nodes: []events.Node{node("in", events.NodeInput), node("in2", events.NodeInput), node("out", events.NodeOutput)},
		}, true},
		{"no output", events.Graph{
			Nodes: []events.Node{node("in", events.NodeInput), node("r", events.NodeRule)},
			Edges: []events.Edge{{From: "in", To: "r"}},
		}, true},
		{"edge to unknown node", events.Graph{
			Nodes: []events.Node{node("in", events.NodeInput), node("out", events.NodeOutput)},
			Edges: []events.Edge{{From: "in", To: "ghost"}},
		}, true},
		{"self loop", events.Graph{
			Nodes: []events.Node{node("in", events.NodeInput), node("r", events.NodeRule), node("out", events.NodeOutput)},
			Edges: []events.Edge{{From: "r", To: "r"}, {From: "in", To: "r"}, {From: "r", To: "out"}},
		}, true},
		{"cycle", events.Graph{
			Nodes: []events.Node{node("in", events.NodeInput), node("a", events.NodeRule), node("b", events.NodeRule), node("out", events.NodeOutput)},
			Edges: []events.Edge{{From: "in", To: "a"}, {From: "a", To: "b"}, {From: "b", To: "a"}, {From: "a", To: "out"}},
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := domain.ValidateGraph(c.graph)
			if c.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
		})
	}
}

func TestEtag(t *testing.T) {
	g := events.Graph{
		Nodes: []events.Node{node("in", events.NodeInput), node("out", events.NodeOutput)},
		Edges: []events.Edge{{From: "in", To: "out"}},
	}
	a, err := domain.Etag(g, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := domain.Etag(g, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("etag not deterministic: %q != %q", a, b)
	}
	withSchema, err := domain.Etag(g, []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	if withSchema == a {
		t.Fatal("etag should depend on the input schema")
	}
	g.Nodes = append(g.Nodes, node("r", events.NodeRule))
	changed, err := domain.Etag(g, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed == a {
		t.Fatal("etag should change when the graph changes")
	}
}

func TestCommandValidate(t *testing.T) {
	if err := (domain.CreateFlow{Slug: "good-slug-1", Name: "Flow"}).Validate(); err != nil {
		t.Fatalf("valid create rejected: %v", err)
	}
	for _, bad := range []string{"", "Bad Slug", "UPPER", "-leading", "trailing-", "under_score"} {
		if err := (domain.CreateFlow{Slug: bad, Name: "Flow"}).Validate(); err == nil {
			t.Fatalf("slug %q should be rejected", bad)
		}
	}
	if err := (domain.CreateFlow{Slug: "ok", Name: "  "}).Validate(); err == nil {
		t.Fatal("blank name should be rejected")
	}
	if err := (domain.PublishVersion{FlowID: "", Graph: events.Graph{Nodes: []events.Node{node("in", events.NodeInput), node("out", events.NodeOutput)}}}).Validate(); err == nil {
		t.Fatal("empty flow id should be rejected")
	}
}

func TestDeployVersionValidate(t *testing.T) {
	ok := domain.DeployVersion{FlowID: "f", Environment: "production", Version: 1}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid deploy rejected: %v", err)
	}
	ab := domain.DeployVersion{FlowID: "f", Environment: "sandbox", Version: 1, ChallengerVersion: 2, ChallengerPct: 50}
	if err := ab.Validate(); err != nil {
		t.Fatalf("valid A/B deploy rejected: %v", err)
	}
	bad := []domain.DeployVersion{
		{FlowID: "", Environment: "production", Version: 1},                                            // no flow
		{FlowID: "f", Environment: "staging", Version: 1},                                              // bad env
		{FlowID: "f", Environment: "production", Version: 0},                                           // version < 1
		{FlowID: "f", Environment: "production", Version: 1, ChallengerPct: 50},                        // pct without challenger
		{FlowID: "f", Environment: "production", Version: 1, ChallengerVersion: 2, ChallengerPct: 150}, // pct out of range
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Fatalf("case %d: expected validation error for %+v", i, c)
		}
	}
}
