// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/decision-engine/models"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

// TestDecidePreResolvesPredictNode exercises the full registry → evaluate → inject →
// record path: a logistic model is registered, the shell scores the input, branches
// on the prediction, and records it for replay.
func TestDecidePreResolvesPredictNode(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFlow(t, ctx, log, st, id, "score", "Score", flowtest.PredictGraph())

	// Register a logistic model and materialize the registry read model.
	if _, err := command.NewHandler(log).DefineModel(ctx, id, "risk",
		json.RawMessage(`{"kind":"logistic","intercept":-3,"coefficients":{"fico":0.005}}`)); err != nil {
		t.Fatal(err)
	}
	if err := projection.New(log, st, models.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}

	dh := command.NewDecideHandler(log, st, command.WithModels(models.Provider{Store: st}))

	// fico=700: z = -3 + 0.005*700 = 0.5; sigmoid(0.5) ≈ 0.62 >= 0.5 -> high.
	res, err := dh.Decide(ctx, id, "score", "production", map[string]any{"fico": 700}, command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StatusCompleted || res.Output["tier"] != "high" {
		t.Fatalf("want high, got %+v (%s)", res.Output, res.Error)
	}

	// fico=400: z = -3 + 2 = -1; sigmoid(-1) ≈ 0.27 < 0.5 -> low.
	low, err := dh.Decide(ctx, id, "score", "production", map[string]any{"fico": 400}, command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if low.Status != domain.StatusCompleted || low.Output["tier"] != "low" {
		t.Fatalf("want low, got %+v (%s)", low.Output, low.Error)
	}

	// The prediction is recorded in the decision's input (replay reads it).
	evs, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawPredict bool
	for _, e := range evs {
		if e.Type != events.TypeDecisionStarted {
			continue
		}
		var p events.DecisionStarted
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		if err := json.Unmarshal(p.Data, &data); err != nil {
			t.Fatal(err)
		}
		pred, ok := data["predict"].(map[string]any)
		if !ok {
			t.Fatalf("predict data not recorded: %v", data)
		}
		if _, ok := pred["risk"]; !ok {
			t.Fatalf("risk prediction not recorded: %v", pred)
		}
		sawPredict = true
	}
	if !sawPredict {
		t.Fatal("no DecisionStarted recorded")
	}
}

func TestDecidePredictNodeWithoutProviderFailsLoudly(t *testing.T) {
	decideFailsWithoutProvider(t, "score", flowtest.PredictGraph())
}
