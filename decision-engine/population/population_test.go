// SPDX-License-Identifier: AGPL-3.0-or-later

package population_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/decision-engine/population"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func waitApplied(t *testing.T, runtime *projection.Runtime, log eventlog.Log) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.Applied() < log.Head() {
		if err := runtime.Err(); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("projection stopped at %d, log head is %d", runtime.Applied(), log.Head())
		}
		time.Sleep(time.Millisecond)
	}
}

func publishFlow(t *testing.T, ctx context.Context, log eventlog.Log, runtime *projection.Runtime, id identity.Identity) string {
	t.Helper()
	handler := command.NewHandler(log)
	flowID, _, err := handler.CreateFlow(ctx, id, domain.CreateFlow{Slug: "offers", Name: "Offers"})
	if err != nil {
		t.Fatal(err)
	}
	waitApplied(t, runtime, log)
	_, _, _, err = handler.PublishVersion(ctx, id, domain.PublishVersion{
		FlowID: flowID, Graph: flowtest.ConstGraph("approve"),
	})
	if err != nil {
		t.Fatal(err)
	}
	waitApplied(t, runtime, log)
	return flowID
}

func TestBacktestJobPinsManifestCompletesAndReplays(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := eventlog.NewMemory()
	st := store.NewMemory()
	runtime := projection.New(log, st, flows.Projector{}, population.Projector{})
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "operator"}
	flowID := publishFlow(t, ctx, log, runtime, id)

	decide := command.NewDecideHandler(log, st)
	handler := population.NewHandler(log, st, decide, nil)
	created, event, err := handler.Create(ctx, id, population.CreateCommand{
		Kind: population.KindBacktest, Slug: "offers", Environment: "sandbox",
		Items: []population.ItemInput{
			{Data: map[string]any{"customer_id": "a"}},
			{Data: map[string]any{"customer_id": "b"}},
			{Data: map[string]any{"customer_id": "c"}},
		},
		Concurrency: 2, MaxAttempts: 2, RetentionDays: 7,
	}, "job-key")
	if err != nil || event.Seq == 0 {
		t.Fatalf("create: event=%+v err=%v", event, err)
	}
	if created.Manifest.FlowID != flowID || created.Manifest.Items[0].Version != 1 {
		t.Fatalf("manifest did not pin the published version: %+v", created.Manifest)
	}
	waitApplied(t, runtime, log)

	for tick := 0; tick < 10; tick++ {
		worked, err := handler.Tick(ctx, "worker-a")
		if err != nil {
			t.Fatal(err)
		}
		waitApplied(t, runtime, log)
		view, ok, err := population.Read(ctx, st, id, created.JobID)
		if err != nil || !ok {
			t.Fatalf("read job: ok=%v err=%v", ok, err)
		}
		if view.State == population.StateCompleted {
			break
		}
		if !worked {
			t.Fatalf("worker became idle before completion: %+v", view)
		}
	}
	view, ok, err := population.Read(ctx, st, id, created.JobID)
	if err != nil || !ok || view.State != population.StateCompleted ||
		view.Succeeded != 3 || view.Pending != 0 || view.Running != 0 {
		t.Fatalf("completed projection: ok=%v err=%v view=%+v", ok, err, view)
	}
	for _, item := range view.Items {
		if item.State != "succeeded" || item.Output["decision"] != "approve" {
			t.Fatalf("item was not completed exactly once: %+v", item)
		}
	}
	decisions, err := log.ReadTenantStream(ctx, id.Org, id.Workspace, "decision.decisions", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 0 {
		t.Fatalf("backtest persisted %d decision events", len(decisions))
	}

	replayed := store.NewMemory()
	replay := projection.New(log, replayed, flows.Projector{}, population.Projector{})
	if err := replay.Start(ctx); err != nil {
		t.Fatal(err)
	}
	replayedView, ok, err := population.Read(ctx, replayed, id, created.JobID)
	if err != nil || !ok || replayedView.ManifestHash != view.ManifestHash ||
		replayedView.State != population.StateCompleted || replayedView.Succeeded != 3 {
		t.Fatalf("replay diverged: ok=%v err=%v view=%+v", ok, err, replayedView)
	}
}

func TestExpiredWorkerOutcomeIsFencedBySuccessorClaim(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "worker"}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	appendEvent := func(typ string, payload any, unique string) eventlog.Envelope {
		t.Helper()
		event, err := eventlog.AppendJSONUnique(
			ctx, log, id.Org, id.Workspace, id.Actor,
			population.Stream, typ, now, payload, unique,
		)
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	created := population.Created{
		JobID: "job-1",
		Manifest: population.Manifest{
			Kind: population.KindDecision, FlowID: "flow-1", Slug: "offers",
			Environment: "sandbox",
			Items:       []population.ItemManifest{{Index: 0, Version: 1}},
		},
		ManifestHash: "manifest", MaxAttempts: 2, Concurrency: 1,
		ExpiresAt: now.AddDate(0, 0, 30),
	}
	appendEvent(population.TypeCreated, created, "created")
	appendEvent(population.TypeItemClaimed, population.ItemClaimed{
		JobID: "job-1", Index: 0, Attempt: 1, Worker: "replica-a",
		LeaseUntil: now.Add(30 * time.Second),
	}, "claim-1")
	appendEvent(population.TypeItemClaimed, population.ItemClaimed{
		JobID: "job-1", Index: 0, Attempt: 2, Worker: "replica-b",
		LeaseUntil: now.Add(time.Minute),
	}, "claim-2")
	// The killed/expired replica reports late. The fact remains in the audit
	// stream but must not replace the active successor's result.
	appendEvent(population.TypeItemSucceeded, population.ItemSucceeded{
		JobID: "job-1", Index: 0, Attempt: 1, Status: "completed",
		Output: map[string]any{"owner": "replica-a"},
	}, "outcome-1")
	appendEvent(population.TypeItemSucceeded, population.ItemSucceeded{
		JobID: "job-1", Index: 0, Attempt: 2, Status: "completed",
		Output: map[string]any{"owner": "replica-b"},
	}, "outcome-2")
	_, err := eventlog.AppendJSONUnique(
		ctx, log, id.Org, id.Workspace, id.Actor,
		population.Stream, population.TypeItemClaimed, now,
		population.ItemClaimed{JobID: "job-1", Index: 0, Attempt: 2, Worker: "replica-c"},
		"claim-2",
	)
	if !errors.Is(err, eventlog.ErrConflict) {
		t.Fatalf("duplicate replica claim error = %v, want conflict", err)
	}

	st := store.NewMemory()
	runtime := projection.New(log, st, population.Projector{})
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	view, ok, err := population.Read(ctx, st, id, "job-1")
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	if view.Succeeded != 1 || view.Items[0].Attempt != 2 ||
		view.Items[0].Output["owner"] != "replica-b" {
		raw, _ := json.Marshal(view)
		t.Fatalf("successor did not own the logical item: %s", raw)
	}
}

func TestKilledWorkerLeaseIsRecoveredOnceAcrossReplicas(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := eventlog.NewMemory()
	st := store.NewMemory()
	runtime := projection.New(log, st, flows.Projector{}, population.Projector{})
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "operator"}
	publishFlow(t, ctx, log, runtime, id)

	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	handler := population.NewHandler(log, st, command.NewDecideHandler(log, st), nil).
		WithNow(func() time.Time { return now })
	created, _, err := handler.Create(ctx, id, population.CreateCommand{
		Kind: population.KindBacktest, Slug: "offers", Environment: "sandbox",
		Items:       []population.ItemInput{{Data: map[string]any{"customer_id": "a"}}},
		Concurrency: 1, MaxAttempts: 2, RetentionDays: 7,
	}, "recover-job")
	if err != nil {
		t.Fatal(err)
	}
	waitApplied(t, runtime, log)

	// Replica A dies after its durable claim and before producing an outcome.
	_, err = eventlog.AppendJSONUnique(
		ctx, log, id.Org, id.Workspace, "population-worker:replica-a",
		population.Stream, population.TypeItemClaimed, now,
		population.ItemClaimed{
			JobID: created.JobID, Index: 0, Attempt: 1, Worker: "replica-a",
			LeaseUntil: now.Add(time.Second),
		},
		"killed-worker-claim",
	)
	if err != nil {
		t.Fatal(err)
	}
	waitApplied(t, runtime, log)
	now = now.Add(time.Minute)

	// Two replacement replicas race the expired lease. The durable attempt claim
	// admits one owner, and only that owner may write the logical item result.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, owner := range []string{"replica-b", "replica-c"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, tickErr := handler.Tick(ctx, owner)
			errs <- tickErr
		}()
	}
	wg.Wait()
	close(errs)
	for tickErr := range errs {
		if tickErr != nil {
			t.Fatal(tickErr)
		}
	}
	waitApplied(t, runtime, log)
	if _, err := handler.Tick(ctx, "replica-b"); err != nil {
		t.Fatal(err)
	}
	waitApplied(t, runtime, log)

	view, ok, err := population.Read(ctx, st, id, created.JobID)
	if err != nil || !ok || view.State != population.StateCompleted ||
		view.Succeeded != 1 || view.Items[0].Attempt != 2 {
		t.Fatalf("recovered job: ok=%v err=%v view=%+v", ok, err, view)
	}
	all, err := log.ReadTenantStream(ctx, id.Org, id.Workspace, population.Stream, 0)
	if err != nil {
		t.Fatal(err)
	}
	claims, outcomes := 0, 0
	for _, event := range all {
		switch event.Type {
		case population.TypeItemClaimed:
			var claim population.ItemClaimed
			if err := json.Unmarshal(event.Payload, &claim); err != nil {
				t.Fatal(err)
			}
			if claim.JobID == created.JobID && claim.Attempt == 2 {
				claims++
			}
		case population.TypeItemSucceeded:
			var outcome population.ItemSucceeded
			if err := json.Unmarshal(event.Payload, &outcome); err != nil {
				t.Fatal(err)
			}
			if outcome.JobID == created.JobID && outcome.Attempt == 2 {
				outcomes++
			}
		}
	}
	if claims != 1 || outcomes != 1 {
		t.Fatalf("successor attempt facts: claims=%d outcomes=%d", claims, outcomes)
	}
}

func TestRetentionSchedulerExpiresResultBodiesIdempotently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := eventlog.NewMemory()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "operator"}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	runtime := projection.New(log, st, population.Projector{})
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.PutDoc(ctx, st, population.Collection, store.Key(id.Org, id.Workspace, "job-expired"), population.View{
		Org: id.Org, Workspace: id.Workspace, JobID: "job-expired",
		State: population.StateCompleted, StateToken: "completed-event",
		Items: []population.ItemView{{
			Index: 0, State: "succeeded", Output: map[string]any{"decision": "approve"},
		}},
		Total: 1, Succeeded: 1, ExpiresAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	scheduler := population.NewScheduler(log, st).WithNow(func() time.Time { return now })
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	waitApplied(t, runtime, log)
	view, ok, err := population.Read(ctx, st, id, "job-expired")
	if err != nil || !ok || view.State != population.StateExpired ||
		view.Items[0].Output != nil {
		t.Fatalf("expired result: ok=%v err=%v view=%+v", ok, err, view)
	}
	all, err := log.ReadTenantStream(ctx, id.Org, id.Workspace, population.Stream, 0)
	if err != nil {
		t.Fatal(err)
	}
	expired := 0
	for _, event := range all {
		if event.Type == population.TypeExpired {
			expired++
		}
	}
	if expired != 1 {
		t.Fatalf("expiration facts = %d, want exactly one", expired)
	}
}
