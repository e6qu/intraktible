// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

// publishDecisionFlow creates and publishes the executable decision flow.
// Projections are rebuilt inside publishFlow.
func publishDecisionFlow(t *testing.T, ctx context.Context, log eventlog.Log, st store.Store, id identity.Identity) {
	t.Helper()
	publishFlow(t, ctx, log, st, id, "scoring", "Scoring", flowtest.DecisionGraph())
}

func TestDecideAndHistoryReplay(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}

	st := store.NewMemory()
	publishDecisionFlow(t, ctx, log, st, id)

	dh := command.NewDecideHandler(log, st)
	res, err := dh.Decide(ctx, id, "scoring", "production", map[string]any{"fico": 680, "bonus": 40}, command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StatusCompleted {
		t.Fatalf("status=%s err=%s", res.Status, res.Error)
	}
	if res.Output["decision"] != "APPROVE" {
		t.Fatalf("decision=%v, want APPROVE", res.Output["decision"])
	}

	// Rebuild the decision-history read model purely from the event stream.
	hist := store.NewMemory()
	if err := projection.New(log, hist, history.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := history.Read(ctx, hist, id, res.DecisionID)
	if err != nil || !ok {
		t.Fatalf("history read: ok=%v err=%v", ok, err)
	}
	if rec.Status != "completed" || rec.Slug != "scoring" || rec.Version != 1 {
		t.Fatalf("record meta: %+v", rec)
	}
	// The decline branch must not be on the recorded path.
	want := []string{"in", "score", "s", "approve", "out"}
	if len(rec.TimeOrdered) != len(want) {
		t.Fatalf("time_ordered=%v, want %v", rec.TimeOrdered, want)
	}
	for i, n := range want {
		if rec.TimeOrdered[i] != n {
			t.Fatalf("time_ordered=%v, want %v", rec.TimeOrdered, want)
		}
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out["decision"] != "APPROVE" {
		t.Fatalf("recorded output=%v", out)
	}
}

func TestDecideFailureIsRecorded(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishDecisionFlow(t, ctx, log, st, id)

	dh := command.NewDecideHandler(log, st)
	// "fico" is absent from the data -> the assignment expression fails loudly.
	res, err := dh.Decide(ctx, id, "scoring", "sandbox", map[string]any{"bonus": 40}, command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StatusFailed || res.Error == "" {
		t.Fatalf("expected a recorded failure, got %+v", res)
	}

	hist := store.NewMemory()
	if err := projection.New(log, hist, history.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	rec, ok, _ := history.Read(ctx, hist, id, res.DecisionID)
	if !ok || rec.Status != "failed" || rec.Error == "" {
		t.Fatalf("history did not record the failure: %+v", rec)
	}
}

func TestDecideUnknownFlowAndEnv(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishDecisionFlow(t, ctx, log, st, id)
	dh := command.NewDecideHandler(log, st)

	if _, err := dh.Decide(ctx, id, "ghost", "production", nil, command.EntityRef{}); err == nil {
		t.Fatal("expected error for unknown flow")
	}
	if _, err := dh.Decide(ctx, id, "scoring", "qa", nil, command.EntityRef{}); err == nil {
		t.Fatal("expected error for invalid environment")
	}
}
