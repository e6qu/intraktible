// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/platform/entity"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

// stubFeatures is a fixed feature source, proving the decide path injects features
// without any dependency on the Context Layer.
type stubFeatures map[string]float64

func (s stubFeatures) Features(_ context.Context, _ identity.Identity, _ entity.Ref) (map[string]float64, error) {
	return s, nil
}

// publishFlow creates + publishes a flow and rebuilds the flow registry the decide
// path reads. Shared by the decide/feature tests so the boilerplate lives once.
func publishFlow(t *testing.T, ctx context.Context, log eventlog.Log, st store.Store, id identity.Identity, slug, name string, graph events.Graph) {
	t.Helper()
	h := command.NewHandler(log)
	flowID, _, err := h.CreateFlow(ctx, id, domain.CreateFlow{Slug: slug, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.PublishVersion(ctx, id, domain.PublishVersion{FlowID: flowID, Graph: graph}); err != nil {
		t.Fatal(err)
	}
	if err := projection.New(log, st, flows.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
}

func publishFeatureFlow(t *testing.T, ctx context.Context, log eventlog.Log, st store.Store, id identity.Identity) {
	t.Helper()
	publishFlow(t, ctx, log, st, id, "risk", "Risk", flowtest.FeatureGraph())
}

func TestDecideInjectsFeatures(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFeatureFlow(t, ctx, log, st, id)

	ref := command.EntityRef{Type: "customer", ID: "c1"}

	// 5 >= 3 -> high.
	dhHigh := command.NewDecideHandler(log, st, command.WithFeatures(stubFeatures{"txn_count_24h": 5}))
	res, err := dhHigh.Decide(ctx, id, "risk", "production", nil, ref)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StatusCompleted || res.Output["tier"] != "high" {
		t.Fatalf("want high, got %+v (%s)", res.Output, res.Error)
	}

	// 1 >= 3 is false -> low.
	dhLow := command.NewDecideHandler(log, st, command.WithFeatures(stubFeatures{"txn_count_24h": 1}))
	res, err = dhLow.Decide(ctx, id, "risk", "production", nil, ref)
	if err != nil {
		t.Fatal(err)
	}
	if res.Output["tier"] != "low" {
		t.Fatalf("want low, got %+v", res.Output)
	}

	// The recorded DecisionStarted carries the entity ref and the injected
	// features in its data — so the run replays from the log alone.
	evs, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range evs {
		if e.Type != events.TypeDecisionStarted {
			continue
		}
		var p events.DecisionStarted
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.EntityType != "customer" || p.EntityID != "c1" {
			t.Fatalf("entity ref not recorded: %+v", p)
		}
		var data map[string]any
		if err := json.Unmarshal(p.Data, &data); err != nil {
			t.Fatal(err)
		}
		feats, ok := data["features"].(map[string]any)
		if !ok || feats["txn_count_24h"] == nil {
			t.Fatalf("features not recorded in data: %v", data)
		}
		found = true
	}
	if !found {
		t.Fatal("no DecisionStarted event recorded")
	}
}

// TestDecideWithoutEntityRefSkipsFeatures confirms the provider is ignored when no
// entity is referenced (the feature flow then fails loudly on the missing var).
func TestDecideWithoutEntityRefSkipsFeatures(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFeatureFlow(t, ctx, log, st, id)

	dh := command.NewDecideHandler(log, st, command.WithFeatures(stubFeatures{"txn_count_24h": 5}))
	res, err := dh.Decide(ctx, id, "risk", "production", nil, command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StatusFailed {
		t.Fatalf("expected failure without features, got %+v", res)
	}
}
