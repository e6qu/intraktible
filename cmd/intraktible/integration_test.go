// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/agent-manager/agents"
	agentcmd "github.com/e6qu/intraktible/agent-manager/command"
	agentdomain "github.com/e6qu/intraktible/agent-manager/domain"
	contextcmd "github.com/e6qu/intraktible/context-layer/command"
	"github.com/e6qu/intraktible/context-layer/connectors"
	contextdomain "github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/context-layer/features"
	enginecmd "github.com/e6qu/intraktible/decision-engine/command"
	enginedomain "github.com/e6qu/intraktible/decision-engine/domain"
	engineevents "github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

// TestFeaturesDriveDecision wires the real Context Layer feature provider into the
// decision engine — the cross-component path the composition root assembles — and
// proves a flow's Rule reads features computed from recorded context events.
func TestFeaturesDriveDecision(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()

	// Context Layer: define a feature and record three transactions for the entity.
	cc := contextcmd.NewHandler(log)
	if _, err := cc.DefineFeature(ctx, id, contextdomain.DefineFeature{
		Name: "txn_count_24h", EntityType: "customer", EventName: "transaction", Aggregation: "count", WindowHours: 24,
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := cc.RecordEvent(ctx, id, contextdomain.RecordEvent{
			EntityType: "customer", EntityID: "c1", EventName: "transaction",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Decision Engine: publish the feature-driven flow.
	ec := enginecmd.NewHandler(log)
	flowID, _, err := ec.CreateFlow(ctx, id, enginedomain.CreateFlow{Slug: "risk", Name: "Risk"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ec.PublishVersion(ctx, id, enginedomain.PublishVersion{FlowID: flowID, Graph: featureGraph()}); err != nil {
		t.Fatal(err)
	}

	// Rebuild the read models both components read at decide time.
	if err := projection.New(log, st, flows.Projector{}, entities.Projector{}, features.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}

	// The composition-root wiring: the engine's feature provider IS the Context
	// Layer's adapter over the shared store.
	dh := enginecmd.NewDecideHandler(log, st, enginecmd.WithFeatures(features.Provider{Store: st}))
	res, err := dh.Decide(ctx, id, "risk", "production", nil, enginecmd.EntityRef{Type: "customer", ID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	// 3 transactions >= 3 -> high tier, driven entirely by the computed feature.
	if res.Status != string(enginedomain.StatusCompleted) || res.Output["tier"] != "high" {
		out, _ := json.Marshal(res)
		t.Fatalf("feature did not drive the decision: %s", out)
	}
}

// TestConnectorDrivesDecision wires the real Context Layer connector provider into
// the decision engine and proves a flow's Connect node fetches from a defined
// connector, with the response injected into the flow.
func TestConnectorDrivesDecision(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()

	cc := contextcmd.NewHandler(log)
	if _, err := cc.DefineConnector(ctx, id, contextdomain.DefineConnector{Name: "bureau", Type: "mock_bureau"}); err != nil {
		t.Fatal(err)
	}

	ec := enginecmd.NewHandler(log)
	flowID, _, err := ec.CreateFlow(ctx, id, enginedomain.CreateFlow{Slug: "screen", Name: "Screen"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ec.PublishVersion(ctx, id, enginedomain.PublishVersion{FlowID: flowID, Graph: connectGraph()}); err != nil {
		t.Fatal(err)
	}
	if err := projection.New(log, st, flows.Projector{}, connectors.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}

	dh := enginecmd.NewDecideHandler(log, st, enginecmd.WithConnectors(connectors.Provider{Store: st}))
	res, err := dh.Decide(ctx, id, "screen", "production", map[string]any{"subject": "Acme Corp"}, enginecmd.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != string(enginedomain.StatusCompleted) {
		t.Fatalf("status=%s err=%s", res.Status, res.Error)
	}
	conn, ok := res.Output["connect"].(map[string]any)
	if !ok {
		t.Fatalf("no connect data in output: %+v", res.Output)
	}
	bureau, ok := conn["bureau"].(map[string]any)
	if !ok || bureau["risk_score"] == nil {
		t.Fatalf("bureau response not injected: %+v", conn)
	}
}

// connectGraph: input -> connect(bureau) -> output(whole context, incl. connect.*).
func connectGraph() engineevents.Graph {
	return engineevents.Graph{
		Nodes: []engineevents.Node{
			{ID: "in", Type: engineevents.NodeInput},
			{ID: "c", Type: engineevents.NodeConnect, Config: json.RawMessage(`{"connector":"bureau","output":"bureau"}`)},
			{ID: "out", Type: engineevents.NodeOutput},
		},
		Edges: []engineevents.Edge{{From: "in", To: "c"}, {From: "c", To: "out"}},
	}
}

// TestAgentDrivesDecision wires the real Agent Manager provider into the decision
// engine and proves a flow's AI node runs a defined agent, with its output
// injected into the flow.
func TestAgentDrivesDecision(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	st := store.NewMemory()

	reg := ai.NewRegistry()
	reg.Register(ai.Stub{})

	ac := agentcmd.NewHandler(log, st, reg)
	if _, err := ac.DefineAgent(ctx, id, agentdomain.DefineAgent{Name: "assess", System: "assess risk"}); err != nil {
		t.Fatal(err)
	}

	ec := enginecmd.NewHandler(log)
	flowID, _, err := ec.CreateFlow(ctx, id, enginedomain.CreateFlow{Slug: "assess", Name: "Assess"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ec.PublishVersion(ctx, id, enginedomain.PublishVersion{FlowID: flowID, Graph: aiGraph()}); err != nil {
		t.Fatal(err)
	}
	if err := projection.New(log, st, flows.Projector{}, agents.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}

	dh := enginecmd.NewDecideHandler(log, st, enginecmd.WithAgents(agents.Provider{Store: st, Registry: reg}))
	res, err := dh.Decide(ctx, id, "assess", "production", map[string]any{"q": "why flagged?"}, enginecmd.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != string(enginedomain.StatusCompleted) {
		t.Fatalf("status=%s err=%s", res.Status, res.Error)
	}
	aiOut, ok := res.Output["ai"].(map[string]any)
	if !ok {
		t.Fatalf("no ai data in output: %+v", res.Output)
	}
	assess, ok := aiOut["assess"].(map[string]any)
	if !ok || assess["text"] == nil {
		t.Fatalf("agent output not injected: %+v", aiOut)
	}
}

// aiGraph: input -> ai(assess) -> output(whole context, incl. ai.*).
func aiGraph() engineevents.Graph {
	return engineevents.Graph{
		Nodes: []engineevents.Node{
			{ID: "in", Type: engineevents.NodeInput},
			{ID: "a", Type: engineevents.NodeAI, Config: json.RawMessage(`{"agent":"assess","output":"assess"}`)},
			{ID: "out", Type: engineevents.NodeOutput},
		},
		Edges: []engineevents.Edge{{From: "in", To: "a"}, {From: "a", To: "out"}},
	}
}

// featureGraph is a minimal Rule-driven flow reading an injected feature:
// input -> rule(when features.txn_count_24h >= 3 then tier='high') -> output(tier).
func featureGraph() engineevents.Graph {
	rule := json.RawMessage(`{"rules":[{"when":"features.txn_count_24h >= 3","then":[{"target":"tier","expr":"'high'"}]}]}`)
	return engineevents.Graph{
		Nodes: []engineevents.Node{
			{ID: "in", Type: engineevents.NodeInput},
			{ID: "r", Type: engineevents.NodeRule, Config: rule},
			{ID: "out", Type: engineevents.NodeOutput, Config: json.RawMessage(`{"fields":["tier"]}`)},
		},
		Edges: []engineevents.Edge{{From: "in", To: "r"}, {From: "r", To: "out"}},
	}
}

// The well-known dev admin key is a local-dev convenience and must never be seeded
// onto a durable store — a real deployment uses sqlite/postgres, so it can never
// boot with a known admin credential no matter the flag value.
func TestSeedDevKeyOnlyOnMemoryStore(t *testing.T) {
	const dev = "dev-sandbox-key"
	cases := []struct {
		store string
		want  bool
	}{
		{"memory", true},
		{"sqlite", false},
		{"postgres", false},
	}
	for _, c := range cases {
		kr := auth.NewKeyring()
		if got := seedDevKey(kr, dev, c.store); got != c.want {
			t.Errorf("seedDevKey(store=%q) = %v, want %v", c.store, got, c.want)
		}
		_, resolved := kr.Resolve(dev)
		if resolved != c.want {
			t.Errorf("store=%q: dev key resolvable = %v, want %v", c.store, resolved, c.want)
		}
	}

	// An empty key never seeds, even on memory.
	if seedDevKey(auth.NewKeyring(), "", "memory") {
		t.Error("an empty --dev-api-key must not seed any key")
	}
}
