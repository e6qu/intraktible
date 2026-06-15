// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
)

func TestConnectSpecs(t *testing.T) {
	g := events.Graph{Nodes: []events.Node{
		{ID: "in", Type: events.NodeInput},
		{ID: "c", Type: events.NodeConnect, Config: json.RawMessage(`{"connector":"bureau","output":"b"}`)},
	}}
	specs, err := domain.ConnectSpecs(g)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Connector != "bureau" || specs[0].Output != "b" || specs[0].NodeID != "c" {
		t.Fatalf("specs = %+v", specs)
	}

	// A Connect node missing its connector/output fails loudly.
	bad := events.Graph{Nodes: []events.Node{
		{ID: "c", Type: events.NodeConnect, Config: json.RawMessage(`{"output":"b"}`)},
	}}
	if _, err := domain.ConnectSpecs(bad); err == nil {
		t.Fatal("expected error for connect node without a connector")
	}
}

func TestExecuteConnectNode(t *testing.T) {
	g := events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "c", Type: events.NodeConnect, Config: json.RawMessage(`{"connector":"bureau","output":"bureau"}`)},
			{ID: "out", Type: events.NodeOutput},
		},
		Edges: []events.Edge{{From: "in", To: "c"}, {From: "c", To: "out"}},
	}

	// With the connector pre-resolved (as the shell does), the node passes through.
	input := map[string]any{"connect": map[string]any{"bureau": map[string]any{"score": 80}}}
	run := domain.Execute(g, input)
	if run.Status != domain.StatusCompleted {
		t.Fatalf("status=%s err=%s", run.Status, run.Err)
	}

	// Without pre-resolution, the Connect node fails loudly (no I/O in the core).
	run = domain.Execute(g, map[string]any{})
	if run.Status != domain.StatusFailed || run.FailedNode != "c" {
		t.Fatalf("expected failure at node c, got %+v", run)
	}
}
