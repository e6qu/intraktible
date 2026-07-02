// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/decision-engine/models"
	"github.com/e6qu/intraktible/decision-engine/notify"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

// TestDriftSchedulerPushesToWebhook exercises the whole scheduled-drift-push path
// with the real components: predictions accumulate a per-model histogram, a
// baseline is captured, the distribution then shifts past the PSI threshold, and
// the scheduler delivers the firing edge to a subscribed webhook exactly once
// (deduping on the steady-firing state via the recorded drift-alert event).
func TestDriftSchedulerPushesToWebhook(t *testing.T) {
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
	if _, err := cmd.SetModelMonitor(ctx, id, "risk", 0.1); err != nil {
		t.Fatal(err)
	}
	if err := projection.New(log, st, models.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}

	dh := command.NewDecideHandler(log, st, command.WithModels(models.Provider{Store: st}))
	// Three decisions at fico=700 (probability ≈ 0.62) form the baseline; three at
	// fico=400 (probability ≈ 0.27) then shift the distribution well past threshold.
	for i := 0; i < 3; i++ {
		if _, err := dh.Decide(ctx, id, "score", "sandbox", map[string]any{"fico": 700}, command.EntityRef{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cmd.CaptureModelBaseline(ctx, id, "risk"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := dh.Decide(ctx, id, "score", "sandbox", map[string]any{"fico": 400}, command.EntityRef{}); err != nil {
			t.Fatal(err)
		}
	}

	var hits atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()
	if _, _, err := notify.NewHandler(log).Subscribe(ctx, id, sink.URL, "drift alerts", "", nil); err != nil {
		t.Fatal(err)
	}

	// Fold the predict outputs + baseline into drift stats, and the subscription
	// into the webhook read model. This runtime stays live on the bus, so the
	// drift-alert event the scheduler records below is applied automatically.
	if err := projection.New(log, st, models.DriftProjector{}, notify.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}

	notifier := notify.NewNotifier(log, st, sink.Client())
	sched := models.NewScheduler(st, cmd, notifier, 0)

	// First sweep: PSI crosses the threshold, so it alerts + delivers once.
	sum, err := sched.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if sum.Alerted != 1 || sum.Delivered != 1 {
		t.Fatalf("first sweep should alert+deliver once: %+v", sum)
	}
	if hits.Load() != 1 {
		t.Fatalf("webhook should have been hit once, got %d", hits.Load())
	}

	// The recorded drift-alert event flips Alerting once the projection catches up.
	if !testutil.Eventually(t, func() bool {
		rep, derr := models.Drift(ctx, st, id, "risk", 0)
		return derr == nil && rep.Alerting
	}) {
		t.Fatal("model never marked alerting")
	}

	// Second sweep: still firing, already alerting — dedup, no re-delivery.
	sum2, err := sched.Tick(ctx)
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if sum2.Alerted != 0 || sum2.Delivered != 0 {
		t.Fatalf("second sweep must not re-alert: %+v", sum2)
	}
	if hits.Load() != 1 {
		t.Fatalf("webhook must not be hit again, got %d", hits.Load())
	}
}
