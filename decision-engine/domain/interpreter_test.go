// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
)

func TestInterpreterDoesNotRequestEffectOnUntakenBranch(t *testing.T) {
	t.Parallel()

	g := events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "split", Type: events.NodeSplit, Config: json.RawMessage(`{"condition":"use_bureau"}`)},
			{ID: "bureau", Type: events.NodeConnect, Config: json.RawMessage(`{"connector":"bureau","output":"report"}`)},
			{ID: "out", Type: events.NodeOutput, Config: json.RawMessage(`{"fields":["use_bureau"]}`)},
		},
		Edges: []events.Edge{
			{From: "in", To: "split"},
			{From: "split", To: "bureau", Branch: "yes"},
			{From: "split", To: "out", Branch: "no"},
			{From: "bureau", To: "out"},
		},
	}

	step := domain.AdvanceExecution(context.Background(), g, domain.StartExecution(g, map[string]any{"use_bureau": false}), nil)
	if step.Effect != nil {
		t.Fatalf("untaken connector requested an effect: %+v", step.Effect)
	}
	if step.Run == nil || step.Run.Status != domain.StatusCompleted {
		t.Fatalf("run = %+v", step.Run)
	}
	for _, result := range step.Run.Results {
		if result.NodeID == "bureau" {
			t.Fatalf("untaken connector appeared in trace: %+v", step.Run.Results)
		}
	}
}

func TestInterpreterEffectSeesUpstreamRecordAndFeedsDownstream(t *testing.T) {
	t.Parallel()

	g := events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "assign", Type: events.NodeAssignment, Config: json.RawMessage(`{"assignments":[{"target":"amount","expr":"requested * 2"}]}`)},
			{ID: "bureau", Type: events.NodeConnect, Config: json.RawMessage(`{"connector":"bureau","output":"report"}`)},
			{ID: "out", Type: events.NodeOutput, Config: json.RawMessage(`{"fields":["amount","connect"]}`)},
		},
		Edges: []events.Edge{
			{From: "in", To: "assign"},
			{From: "assign", To: "bureau"},
			{From: "bureau", To: "out"},
		},
	}

	step := domain.AdvanceExecution(context.Background(), g, domain.StartExecution(g, map[string]any{"requested": 21}), nil)
	if step.Effect == nil || step.Effect.NodeID != "bureau" {
		t.Fatalf("effect = %+v", step.Effect)
	}
	if got := step.Effect.Input["amount"]; got != 42 {
		t.Fatalf("effect amount = %#v, want 42", got)
	}

	state, err := domain.ResolveEffect(g, step.State, *step.Effect, map[string]any{"score": 780})
	if err != nil {
		t.Fatal(err)
	}
	done := domain.AdvanceExecution(context.Background(), g, state, nil)
	if done.Run == nil || done.Run.Status != domain.StatusCompleted {
		t.Fatalf("run = %+v", done.Run)
	}
	connect, ok := done.Run.Output["connect"].(map[string]any)
	if !ok {
		t.Fatalf("connect output = %#v", done.Run.Output["connect"])
	}
	report, ok := connect["report"].(map[string]any)
	if !ok || report["score"] != 780 {
		t.Fatalf("report = %#v", connect["report"])
	}
}

func TestResumeInterpreterRequestsDownstreamEffectAfterReview(t *testing.T) {
	t.Parallel()

	g := events.Graph{
		Nodes: []events.Node{
			{ID: "agent", Type: events.NodeAI, Config: json.RawMessage(`{"agent":"review","version":3,"output":"summary"}`)},
			{ID: "out", Type: events.NodeOutput},
		},
		Edges: []events.Edge{{From: "agent", To: "out"}},
	}
	suspend := domain.SuspendState{
		Resume:    "agent",
		OutputKey: "review",
		Record:    map[string]any{"application_id": "app-7"},
	}
	state := domain.ResumeExecution(suspend, map[string]any{"decision": "approve"})
	step := domain.AdvanceExecution(context.Background(), g, state, nil)
	if step.Effect == nil || step.Effect.Kind != domain.EffectAI {
		t.Fatalf("effect = %+v", step.Effect)
	}
	review, ok := step.Effect.Input["review"].(map[string]any)
	if !ok || review["decision"] != "approve" {
		t.Fatalf("review input = %#v", step.Effect.Input["review"])
	}
	if step.Effect.Input["decision"] != "approve" {
		t.Fatalf("flattened decision = %#v", step.Effect.Input["decision"])
	}
}
