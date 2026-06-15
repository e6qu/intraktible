// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"testing"

	contextcmd "github.com/e6qu/intraktible/context-layer/command"
	contextdomain "github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/context-layer/features"
	enginecmd "github.com/e6qu/intraktible/decision-engine/command"
	enginedomain "github.com/e6qu/intraktible/decision-engine/domain"
	engineevents "github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
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
	if res.Status != enginedomain.StatusCompleted || res.Output["tier"] != "high" {
		out, _ := json.Marshal(res)
		t.Fatalf("feature did not drive the decision: %s", out)
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
