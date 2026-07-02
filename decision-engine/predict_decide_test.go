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
	res, err := dh.Decide(ctx, id, "score", "sandbox", map[string]any{"fico": 700}, command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StatusCompleted || res.Output["tier"] != "high" {
		t.Fatalf("want high, got %+v (%s)", res.Output, res.Error)
	}

	// fico=400: z = -3 + 2 = -1; sigmoid(-1) ≈ 0.27 < 0.5 -> low.
	low, err := dh.Decide(ctx, id, "score", "sandbox", map[string]any{"fico": 400}, command.EntityRef{})
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

// TestModelMonitorThresholdPersists proves SetModelMonitor flows through the event
// stream + DriftProjector onto the model's drift report.
func TestModelMonitorThresholdPersists(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()

	cmd := command.NewHandler(log)
	if _, err := cmd.DefineModel(ctx, id, "risk", json.RawMessage(`{"kind":"logistic","coefficients":{"x":1}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.SetModelMonitor(ctx, id, "risk", 0.3); err != nil {
		t.Fatal(err)
	}
	if err := projection.New(log, st, models.DriftProjector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	rep, err := models.Drift(ctx, st, id, "risk", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Threshold != 0.3 {
		t.Fatalf("threshold = %v, want 0.3", rep.Threshold)
	}
}

// TestModelDriftMonitoring proves predictions accumulate into a per-model
// probability histogram, a baseline can be captured, and a post-baseline shift in
// the predicted distribution is detected as PSI > 0.
func TestModelDriftMonitoring(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFlow(t, ctx, log, st, id, "score", "Score", flowtest.PredictGraph())

	cmd := command.NewHandler(log)
	if _, err := cmd.DefineModel(ctx, id, "risk",
		json.RawMessage(`{"kind":"logistic","intercept":-3,"coefficients":{"fico":0.005}}`)); err != nil {
		t.Fatal(err)
	}
	if err := projection.New(log, st, models.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	dh := command.NewDecideHandler(log, st, command.WithModels(models.Provider{Store: st}))

	// Three decisions at fico=700 (probability ≈ 0.62), then capture the baseline,
	// then two at fico=400 (probability ≈ 0.27) — a real shift in the distribution.
	for i := 0; i < 3; i++ {
		if _, err := dh.Decide(ctx, id, "score", "sandbox", map[string]any{"fico": 700}, command.EntityRef{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cmd.CaptureModelBaseline(ctx, id, "risk"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := dh.Decide(ctx, id, "score", "sandbox", map[string]any{"fico": 400}, command.EntityRef{}); err != nil {
			t.Fatal(err)
		}
	}

	// Fold the predict-node outputs + the baseline into the drift stats.
	if err := projection.New(log, st, models.DriftProjector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	rep, err := models.Drift(ctx, st, id, "risk", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Count != 5 {
		t.Fatalf("drift count = %d, want 5", rep.Count)
	}
	if !rep.HasBaseline || rep.PSI == nil {
		t.Fatalf("expected a baseline + PSI, got %+v", rep)
	}
	if *rep.PSI <= 0 {
		t.Fatalf("expected PSI > 0 after a distribution shift, got %v", *rep.PSI)
	}
}
