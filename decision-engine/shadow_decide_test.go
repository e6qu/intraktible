// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

type recordingShadowConnector struct {
	calls []string
}

func (p *recordingShadowConnector) Fetch(
	_ context.Context,
	_ identity.Identity,
	connector string,
	_ json.RawMessage,
) (json.RawMessage, error) {
	p.calls = append(p.calls, connector)
	switch connector {
	case "live-bureau":
		return json.RawMessage(`{"score":20}`), nil
	case "candidate-bureau":
		return json.RawMessage(`{"score":80}`), nil
	default:
		return nil, fmt.Errorf("unexpected connector %q", connector)
	}
}

func connectorGraph(name string) events.Graph {
	graph := flowtest.ConnectGraph()
	for i := range graph.Nodes {
		if graph.Nodes[i].Type == events.NodeConnect {
			graph.Nodes[i].Config = json.RawMessage(
				fmt.Sprintf(`{"connector":%q,"output":"bureau"}`, name),
			)
		}
	}
	return graph
}

func publishShadowPair(
	t *testing.T,
	ctx context.Context,
	log eventlog.Log,
	st store.Store,
	id identity.Identity,
	live, candidate events.Graph,
) string {
	t.Helper()
	h := command.NewHandler(log)
	flowID, _, err := h.CreateFlow(ctx, id, domain.CreateFlow{Slug: "shadow-deps", Name: "Shadow dependencies"})
	if err != nil {
		t.Fatal(err)
	}
	for _, graph := range []events.Graph{live, candidate} {
		if _, _, _, err := h.PublishVersion(ctx, id, domain.PublishVersion{FlowID: flowID, Graph: graph}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Deploy(ctx, id, domain.DeployVersion{
		FlowID: flowID, Environment: "sandbox", Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SetShadow(ctx, id, domain.SetShadow{
		FlowID: flowID, Environment: "sandbox", Version: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := projection.New(log, st, flows.Projector{}, policy.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	return flowID
}

func lastShadowEvent(t *testing.T, ctx context.Context, log eventlog.Log) events.ShadowEvaluated {
	t.Helper()
	all, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Type != events.TypeShadowEvaluated {
			continue
		}
		var evaluated events.ShadowEvaluated
		if err := json.Unmarshal(all[i].Payload, &evaluated); err != nil {
			t.Fatal(err)
		}
		return evaluated
	}
	t.Fatal("no shadow evaluation event")
	return events.ShadowEvaluated{}
}

func shadowEventCount(t *testing.T, ctx context.Context, log eventlog.Log) int {
	t.Helper()
	all, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range all {
		if event.Type == events.TypeShadowEvaluated {
			count++
		}
	}
	return count
}

func TestShadowResolvesCandidateConnectorIndependently(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	flowID := publishShadowPair(
		t, ctx, log, st, id,
		connectorGraph("live-bureau"), connectorGraph("candidate-bureau"),
	)
	if _, err := command.NewHandler(log).SetShadow(ctx, id, domain.SetShadow{
		FlowID: flowID, Environment: "sandbox", Version: 1,
	}); err == nil {
		t.Fatal("assigning the live champion as its own shadow should fail")
	}

	connectors := &recordingShadowConnector{}
	result, err := command.NewDecideHandler(
		log, st, command.WithConnectors(connectors),
	).Decide(ctx, id, "shadow-deps", "sandbox", nil, command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output["tier"] != "low" {
		t.Fatalf("live result = %v, want low", result.Output)
	}
	if len(connectors.calls) != 2 ||
		connectors.calls[0] != "live-bureau" ||
		connectors.calls[1] != "candidate-bureau" {
		t.Fatalf("connector calls = %v, want independent live then candidate calls", connectors.calls)
	}
	evaluated := lastShadowEvent(t, ctx, log)
	if evaluated.Matched || evaluated.ShadowError != "" ||
		evaluated.MatchBasis != events.ShadowMatchOutput ||
		len(evaluated.ChangedFields) != 1 ||
		evaluated.ChangedFields[0] != "tier" {
		t.Fatalf("shadow evidence = %+v, want output divergence on tier", evaluated)
	}
}

func policyGraph(marker string) events.Graph {
	return events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "assign", Type: events.NodeAssignment, Config: json.RawMessage(
				fmt.Sprintf(`{"assignments":[{"target":"score","expr":"80"},{"target":"marker","expr":%q}]}`, "'"+marker+"'"),
			)},
			{ID: "out", Type: events.NodeOutput, Config: json.RawMessage(`{"fields":["score","marker"]}`)},
		},
		Edges: []events.Edge{{From: "in", To: "assign"}, {From: "assign", To: "out"}},
	}
}

func TestShadowComparesGovernedPolicyOutcomeWhenBound(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	publishShadowPair(t, ctx, log, st, id, policyGraph("live"), policyGraph("candidate"))

	policies := policy.NewHandler(log)
	policyID, _, err := policies.CreatePolicy(ctx, id, "Shadow outcome", "shadow-deps")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := policies.PublishVersion(ctx, id, policyID, policy.Spec{
		Rules: []policy.Rule{{
			When: "score >= 50", Disposition: policy.Approve,
			Code: "AUTO", Description: "score clears the governed band",
		}},
		Default: policy.Refer,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := projection.New(log, st, flows.Projector{}, policy.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}

	if _, err := command.NewDecideHandler(log, st).Decide(
		ctx, id, "shadow-deps", "sandbox", nil, command.EntityRef{},
	); err != nil {
		t.Fatal(err)
	}
	evaluated := lastShadowEvent(t, ctx, log)
	if !evaluated.Matched ||
		evaluated.MatchBasis != events.ShadowMatchPolicy ||
		evaluated.PolicyID != policyID ||
		evaluated.LiveDisposition != "approve" ||
		evaluated.ShadowDisposition != "approve" ||
		evaluated.LiveCode != "AUTO" ||
		evaluated.ShadowCode != "AUTO" ||
		evaluated.LiveReason != "score clears the governed band" ||
		evaluated.ShadowReason != "score clears the governed band" ||
		len(evaluated.ChangedFields) != 1 ||
		evaluated.ChangedFields[0] != "marker" {
		t.Fatalf("policy-based shadow evidence = %+v", evaluated)
	}
}

func TestShadowRecordsDeploymentCollisionInsteadOfComparingChampionToItself(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	flowID := publishShadowPair(
		t, ctx, log, st, id,
		flowtest.ConstGraph("live"), flowtest.ConstGraph("candidate"),
	)

	if _, err := command.NewHandler(log).Deploy(ctx, id, domain.DeployVersion{
		FlowID: flowID, Environment: "sandbox", Version: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := projection.New(log, st, flows.Projector{}, policy.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	result, err := command.NewDecideHandler(log, st).Decide(
		ctx, id, "shadow-deps", "sandbox", nil, command.EntityRef{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output["decision"] != "candidate" {
		t.Fatalf("live result = %v, want newly deployed candidate", result.Output)
	}
	evaluated := lastShadowEvent(t, ctx, log)
	if evaluated.LiveVersion != 2 || evaluated.ShadowVersion != 2 ||
		evaluated.ShadowError != "shadow version 2 is now the live champion; choose a different candidate" {
		t.Fatalf("deployment-collision evidence = %+v", evaluated)
	}
}

func TestShadowSkipsABChallengerTraffic(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	h := command.NewHandler(log)
	flowID, _, err := h.CreateFlow(ctx, id, domain.CreateFlow{
		Slug: "shadow-ab", Name: "Shadow A/B cohort",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []string{"champion", "challenger", "shadow"} {
		if _, _, _, err := h.PublishVersion(ctx, id, domain.PublishVersion{
			FlowID: flowID, Graph: flowtest.ConstGraph(result),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Deploy(ctx, id, domain.DeployVersion{
		FlowID: flowID, Environment: "sandbox", Version: 1,
		ChallengerVersion: 2, ChallengerPct: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SetShadow(ctx, id, domain.SetShadow{
		FlowID: flowID, Environment: "sandbox", Version: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := projection.New(log, st, flows.Projector{}, policy.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}

	result, err := command.NewDecideHandler(
		log, st, command.WithRoll(func() int { return 0 }),
	).Decide(ctx, id, "shadow-ab", "sandbox", nil, command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output["decision"] != "challenger" {
		t.Fatalf("A/B result = %v, want challenger", result.Output)
	}
	if count := shadowEventCount(t, ctx, log); count != 0 {
		t.Fatalf("shadow events = %d, want none for challenger traffic", count)
	}
}
