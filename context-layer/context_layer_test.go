// SPDX-License-Identifier: AGPL-3.0-or-later

package contextlayer_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/e6qu/intraktible/context-layer/command"
	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/context-layer/entities"
	contextevents "github.com/e6qu/intraktible/context-layer/events"
	"github.com/e6qu/intraktible/context-layer/features"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func TestGovernedEntityUniquenessIsReplicaSafeAndPolicyAware(t *testing.T) {
	ctx := context.Background()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "data-owner"}
	for iteration := 0; iteration < 20; iteration++ {
		log := eventlog.NewMemory()
		first := command.NewHandler(log)
		second := command.NewHandler(log)
		start := make(chan struct{})
		type result struct {
			entityID string
			err      error
		}
		results := make(chan result, 2)
		var wait sync.WaitGroup
		for entityID, handler := range map[string]*command.Handler{
			"applicant-a": first, "applicant-b": second,
		} {
			wait.Add(1)
			go func(entityID string, handler *command.Handler) {
				defer wait.Done()
				<-start
				_, err := handler.RecordEntity(ctx, id, domain.RecordEntity{
					EntityType: "applicant", EntityID: entityID,
					Attributes: json.RawMessage(`{"government_id":"same"}`),
					SchemaEvidence: domain.SchemaEvidence{
						Action: "block", UniqueClaim: "source.unique\x00entity/applicant\x00tuple",
					},
				})
				results <- result{entityID: entityID, err: err}
			}(entityID, handler)
		}
		close(start)
		wait.Wait()
		close(results)
		var winner string
		failures := 0
		for result := range results {
			if result.err == nil {
				winner = result.entityID
			} else {
				failures++
			}
		}
		if winner == "" || failures != 1 {
			t.Fatalf("iteration %d: winner %q, failures %d", iteration, winner, failures)
		}
		// The durable claim belongs to the logical entity, not one request: an
		// upsert by the winner can retain the same tuple.
		if _, err := first.RecordEntity(ctx, id, domain.RecordEntity{
			EntityType: "applicant", EntityID: winner,
			Attributes: json.RawMessage(`{"government_id":"same","status":"reviewed"}`),
			SchemaEvidence: domain.SchemaEvidence{
				Action: "block", UniqueClaim: "source.unique\x00entity/applicant\x00tuple",
			},
		}); err != nil {
			t.Fatalf("iteration %d: same-owner update: %v", iteration, err)
		}
		_ = log.Close()
	}

	log := eventlog.NewMemory()
	defer func() { _ = log.Close() }()
	handler := command.NewHandler(log)
	claim := "source.unique\x00entity/applicant\x00referred-tuple"
	if _, err := handler.RecordEntity(ctx, id, domain.RecordEntity{
		EntityType: "applicant", EntityID: "owner",
		SchemaEvidence: domain.SchemaEvidence{Action: "block", UniqueClaim: claim},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.RecordEntity(ctx, id, domain.RecordEntity{
		EntityType: "applicant", EntityID: "duplicate",
		SchemaEvidence: domain.SchemaEvidence{Action: "refer", UniqueClaim: claim},
	}); err != nil {
		t.Fatalf("refer policy should admit with evidence: %v", err)
	}
	envelopes, err := log.ReadTenantStream(ctx, id.Org, id.Workspace, contextevents.StreamContext, 0)
	if err != nil {
		t.Fatal(err)
	}
	var duplicate contextevents.EntityRecorded
	if err := json.Unmarshal(envelopes[len(envelopes)-1].Payload, &duplicate); err != nil {
		t.Fatal(err)
	}
	if len(duplicate.SchemaEvidence.Violations) != 1 ||
		duplicate.SchemaEvidence.Violations[0].Code != "unique" {
		t.Fatalf("duplicate evidence = %+v", duplicate.SchemaEvidence)
	}
}

func TestGovernedRelationshipsFollowBlockAndReferPolicy(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "data-owner"}
	handler := command.NewHandler(log)
	if _, err := handler.RecordEntity(ctx, id, domain.RecordEntity{
		EntityType: "merchant", EntityID: "merchant-1",
	}); err != nil {
		t.Fatal(err)
	}
	relationship := domain.RelationshipCheck{
		Field: "merchant_id", TargetEntityType: "merchant", TargetEntityID: "merchant-1",
	}
	if _, err := handler.RecordEvent(ctx, id, domain.RecordEvent{
		EntityType: "payment", EntityID: "payment-1", EventName: "authorized",
		SchemaEvidence: domain.SchemaEvidence{
			Action: "block", RelationshipChecks: []domain.RelationshipCheck{relationship},
		},
	}); err != nil {
		t.Fatalf("known relationship rejected: %v", err)
	}
	relationship.TargetEntityID = "missing"
	if _, err := handler.RecordEvent(ctx, id, domain.RecordEvent{
		EntityType: "payment", EntityID: "payment-2", EventName: "authorized",
		SchemaEvidence: domain.SchemaEvidence{
			Action: "block", RelationshipChecks: []domain.RelationshipCheck{relationship},
		},
	}); err == nil {
		t.Fatal("block policy should reject an unknown relationship target")
	}
	if _, err := handler.RecordEvent(ctx, id, domain.RecordEvent{
		EntityType: "payment", EntityID: "payment-3", EventName: "authorized",
		SchemaEvidence: domain.SchemaEvidence{
			Action: "refer", RelationshipChecks: []domain.RelationshipCheck{relationship},
		},
	}); err != nil {
		t.Fatalf("refer policy should admit an unknown relationship with evidence: %v", err)
	}
	envelopes, err := log.ReadTenantStream(ctx, id.Org, id.Workspace, contextevents.StreamContext, 0)
	if err != nil {
		t.Fatal(err)
	}
	var referred contextevents.EventRecorded
	if err := json.Unmarshal(envelopes[len(envelopes)-1].Payload, &referred); err != nil {
		t.Fatal(err)
	}
	if len(referred.SchemaEvidence.Violations) != 1 ||
		referred.SchemaEvidence.Violations[0].Code != "relationship" {
		t.Fatalf("relationship evidence = %+v", referred.SchemaEvidence)
	}
}

// TestEntityAndEventReplay records an entity, patches it, and records events, then
// rebuilds the read model from the log (offset 0) to prove the projection is a
// pure fold of the durable stream.
func TestEntityAndEventReplay(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}

	h := command.NewHandler(log)
	if _, err := h.RecordEntity(ctx, id, domain.RecordEntity{
		EntityType: "customer", EntityID: "c1", Attributes: json.RawMessage(`{"tier":"silver","country":"US"}`),
	}); err != nil {
		t.Fatal(err)
	}
	// Patch: tier changes, country retained, kyc added.
	if _, err := h.RecordEntity(ctx, id, domain.RecordEntity{
		EntityType: "customer", EntityID: "c1", Attributes: json.RawMessage(`{"tier":"gold","kyc":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	for _, amt := range []string{`{"amount":100}`, `{"amount":250}`} {
		if _, err := h.RecordEvent(ctx, id, domain.RecordEvent{
			EntityType: "customer", EntityID: "c1", EventName: "transaction", Data: json.RawMessage(amt),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// An event about a not-yet-recorded entity auto-creates a shell.
	if _, err := h.RecordEvent(ctx, id, domain.RecordEvent{
		EntityType: "merchant", EntityID: "m9", EventName: "signup",
	}); err != nil {
		t.Fatal(err)
	}

	st := store.NewMemory()
	if err := projection.New(log, st, entities.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}

	c, ok, err := entities.ReadEntity(ctx, st, id, "customer", "c1")
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	var attrs map[string]any
	if err := json.Unmarshal(c.Attributes, &attrs); err != nil {
		t.Fatal(err)
	}
	if attrs["tier"] != "gold" || attrs["country"] != "US" || attrs["kyc"] != true {
		t.Fatalf("merged attributes = %v", attrs)
	}
	if c.EventCount != 2 {
		t.Fatalf("event_count = %d, want 2", c.EventCount)
	}

	evs, err := entities.ListEvents(ctx, st, id, "customer", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Seq <= evs[1].Seq {
		t.Fatalf("events not newest-first: %+v", evs)
	}
	if evs[0].OccurredAt.IsZero() {
		t.Fatal("occurred_at should be filled by the command when omitted")
	}

	// The shell entity exists from the event alone.
	shell, ok, err := entities.ReadEntity(ctx, st, id, "merchant", "m9")
	if err != nil || !ok {
		t.Fatalf("shell read: ok=%v err=%v", ok, err)
	}
	if shell.EventCount != 1 {
		t.Fatalf("shell event_count = %d, want 1", shell.EventCount)
	}

	// Type filter.
	customers, err := entities.ListEntities(ctx, st, id, "customer")
	if err != nil {
		t.Fatal(err)
	}
	if len(customers) != 1 {
		t.Fatalf("customers = %d, want 1", len(customers))
	}
}

func TestEntityHistoryHonorsKnowledgeCutoff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := eventlog.NewMemory()
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "data-owner"}
	current := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	handler := command.NewHandler(log).WithNow(func() time.Time { return current })
	if _, err := handler.RecordEntity(ctx, id, domain.RecordEntity{
		EntityType: "applicant", EntityID: "a1",
		Attributes: json.RawMessage(`{"segment":"thin-file"}`),
	}); err != nil {
		t.Fatal(err)
	}
	cutoff := current.Add(time.Minute)
	current = current.Add(time.Hour)
	if _, err := handler.RecordEntity(ctx, id, domain.RecordEntity{
		EntityType: "applicant", EntityID: "a1",
		Attributes: json.RawMessage(`{"segment":"established"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.RecordEntity(ctx, id, domain.RecordEntity{
		EntityType: "applicant", EntityID: "a2",
		Attributes: json.RawMessage(`{"segment":"new"}`),
	}); err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory()
	if err := projection.New(log, st, entities.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	asKnown, err := entities.ListEntitiesAt(ctx, st, id, "applicant", cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if len(asKnown) != 1 || asKnown[0].EntityID != "a1" ||
		string(asKnown[0].Attributes) != `{"segment":"thin-file"}` {
		t.Fatalf("entities at cutoff = %+v", asKnown)
	}
	currentRows, err := entities.ListEntitiesAt(
		ctx, st, id, "applicant", current.Add(time.Minute),
	)
	if err != nil || len(currentRows) != 2 {
		t.Fatalf("current entity history = %+v, err %v", currentRows, err)
	}
}

// TestFeatureReplay defines features and records events, then rebuilds from the
// log and computes the features — proving the feature engine reads a pure fold.
func TestFeatureReplay(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}

	h := command.NewHandler(log)
	if _, err := h.DefineFeature(ctx, id, domain.DefineFeature{
		Name: "txn_count_24h", EntityType: "customer", EventName: "transaction", Aggregation: "count", WindowHours: 24,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DefineFeature(ctx, id, domain.DefineFeature{
		Name: "txn_sum_24h", EntityType: "customer", EventName: "transaction", Aggregation: "sum", Field: "amount", WindowHours: 24,
	}); err != nil {
		t.Fatal(err)
	}
	for _, amt := range []string{`{"amount":100}`, `{"amount":250}`} {
		if _, err := h.RecordEvent(ctx, id, domain.RecordEvent{
			EntityType: "customer", EntityID: "c1", EventName: "transaction", Data: json.RawMessage(amt),
		}); err != nil {
			t.Fatal(err)
		}
	}

	st := store.NewMemory()
	if err := projection.New(log, st, entities.Projector{}, features.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}

	vals, err := features.Compute(ctx, st, id, "customer", "c1", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, v := range vals {
		got[v.Name] = v.Value
	}
	if got["txn_count_24h"] != 2 || got["txn_sum_24h"] != 350 {
		t.Fatalf("features = %v, want count 2 / sum 350", got)
	}
}

// TestFeatureVersioningAndCache covers the feature store's versioning (a redefinition
// bumps the monotonic version) and its materialized read-through cache (a warm value
// is served without a fold, and invalidates on a new entity event or a redefinition).
func TestFeatureVersioningAndCache(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	h := command.NewHandler(log)
	st := store.NewMemory()
	reproject := func() {
		if err := projection.New(log, st, entities.Projector{}, features.Projector{}).Start(ctx); err != nil {
			t.Fatal(err)
		}
	}
	def := func() {
		if _, err := h.DefineFeature(ctx, id, domain.DefineFeature{
			Name: "txn_count", EntityType: "customer", EventName: "transaction", Aggregation: "count", WindowHours: 24,
		}); err != nil {
			t.Fatal(err)
		}
	}
	record := func(amt string) {
		if _, err := h.RecordEvent(ctx, id, domain.RecordEvent{
			EntityType: "customer", EntityID: "c1", EventName: "transaction", Data: json.RawMessage(amt),
		}); err != nil {
			t.Fatal(err)
		}
	}

	def()
	def() // redefinition
	record(`{"amount":10}`)
	record(`{"amount":20}`)
	reproject()

	// Version bumped to 2 across the two definitions.
	defs, err := features.List(ctx, st, id, "customer")
	if err != nil || len(defs) != 1 {
		t.Fatalf("defs = %+v (err %v)", defs, err)
	}
	if defs[0].Version != 2 {
		t.Fatalf("version = %d, want 2 after a redefinition", defs[0].Version)
	}

	// First read folds; second read (no new events) is served from the cache.
	v1, err := features.ComputeCached(ctx, st, id, "customer", "c1", time.Now().UTC())
	if err != nil || len(v1) != 1 || v1[0].Value != 2 || v1[0].Cached {
		t.Fatalf("first read = %+v (err %v), want count 2 uncached", v1, err)
	}
	v2, err := features.ComputeCached(ctx, st, id, "customer", "c1", time.Now().UTC())
	if err != nil || v2[0].Value != 2 || !v2[0].Cached {
		t.Fatalf("second read = %+v, want count 2 from cache", v2)
	}
	if v2[0].Version != 2 || v2[0].EventCount != 2 {
		t.Fatalf("cached lineage = %+v, want version 2 / 2 events", v2[0])
	}

	// A new entity event invalidates the cache (entity event count changed).
	record(`{"amount":30}`)
	reproject()
	v3, err := features.ComputeCached(ctx, st, id, "customer", "c1", time.Now().UTC())
	if err != nil || v3[0].Value != 3 || v3[0].Cached {
		t.Fatalf("after a new event = %+v, want count 3 recomputed (uncached)", v3)
	}

	// A redefinition invalidates the cache (version changed) — the recomputed value
	// carries the newer version.
	def()
	reproject()
	v4, err := features.ComputeCached(ctx, st, id, "customer", "c1", time.Now().UTC())
	if err != nil || v4[0].Cached || v4[0].Version <= v2[0].Version {
		t.Fatalf("after a redefinition = %+v, want a higher version recomputed (uncached)", v4)
	}
}

func TestCorrectionsAndRetractionsRespectKnowledgeCutoff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := eventlog.NewMemory()
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "data-owner"}
	recordedAt := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	handler := command.NewHandler(log).WithNow(func() time.Time { return recordedAt })

	if _, err := handler.DefineFeature(ctx, id, domain.DefineFeature{
		Name: "txn_sum", EntityType: "customer", EventName: "transaction",
		Aggregation: domain.AggSum, Field: "amount", WindowHours: 72,
	}); err != nil {
		t.Fatal(err)
	}
	occurredAt := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	if _, err := handler.RecordEvent(ctx, id, domain.RecordEvent{
		EntityType: "customer", EntityID: "c1", EventName: "transaction",
		EventID: "txn-1", Data: json.RawMessage(`{"amount":10}`), OccurredAt: occurredAt,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.RecordEvent(ctx, id, domain.RecordEvent{
		EntityType: "customer", EntityID: "c1", EventName: "transaction",
		EventID: "txn-1", Data: json.RawMessage(`{"amount":10}`), OccurredAt: occurredAt,
	}); err == nil {
		t.Fatal("duplicate source event id should fail")
	}

	recordedAt = time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC)
	if _, err := handler.RecordEvent(ctx, id, domain.RecordEvent{
		EntityType: "customer", EntityID: "c1", EventName: "transaction",
		EventID: "txn-1-correction", SupersedesEventID: "txn-1",
		Data: json.RawMessage(`{"amount":20}`), OccurredAt: occurredAt,
	}); err != nil {
		t.Fatal(err)
	}
	recordedAt = time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC)
	if _, err := handler.RetractEvent(ctx, id, domain.RetractEvent{
		EventID: "txn-1-correction", Reason: "source reversed transaction",
	}); err != nil {
		t.Fatal(err)
	}

	st := store.NewMemory()
	if err := projection.New(log, st, entities.Projector{}, features.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	assertValue := func(knowledgeAt time.Time, want float64) {
		t.Helper()
		values, err := features.ComputeAt(ctx, st, id, "customer", "c1", asOf, knowledgeAt)
		if err != nil {
			t.Fatal(err)
		}
		if len(values) != 1 || values[0].Value != want {
			t.Fatalf("knowledge_at %s values = %+v, want %v", knowledgeAt, values, want)
		}
	}
	assertValue(time.Date(2026, 1, 2, 13, 0, 0, 0, time.UTC), 10)
	assertValue(time.Date(2026, 1, 3, 13, 0, 0, 0, time.UTC), 20)
	assertValue(time.Date(2026, 1, 4, 13, 0, 0, 0, time.UTC), 0)

	history, err := entities.ListEvents(ctx, st, id, "customer", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Status != "retracted" || history[1].Status != "superseded" {
		t.Fatalf("correction history = %+v", history)
	}
}

// TestConnectorReplay defines a connector and records a fetch, then rebuilds from
// the log and confirms the definition and the recorded result survive the replay.
func TestConnectorReplay(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}

	h := command.NewHandler(log)
	if _, err := h.DefineConnector(ctx, id, domain.DefineConnector{Name: "bureau", Type: "mock_bureau"}); err != nil {
		t.Fatal(err)
	}
	// Invoke against a store the connector can read its definition from, then
	// record the result — the effect happens once, the response is logged.
	st := store.NewMemory()
	if err := projection.New(log, st, connectors.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	resp, err := connectors.Invoke(ctx, st, id, "bureau", json.RawMessage(`{"subject":"Acme"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.RecordFetch(ctx, id, "bureau", json.RawMessage(`{"subject":"Acme"}`), resp); err != nil {
		t.Fatal(err)
	}

	// Rebuild a fresh read model purely from the log.
	rebuilt := store.NewMemory()
	if err := projection.New(log, rebuilt, connectors.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	def, ok, err := connectors.Read(ctx, rebuilt, id, "bureau")
	if err != nil || !ok || def.Type != "mock_bureau" {
		t.Fatalf("connector def after replay: ok=%v def=%+v err=%v", ok, def, err)
	}
	fetches, err := connectors.ListFetches(ctx, rebuilt, id, "bureau")
	if err != nil {
		t.Fatal(err)
	}
	if len(fetches) != 1 || !json.Valid(fetches[0].Response) {
		t.Fatalf("recorded fetch did not survive replay: %+v", fetches)
	}
}
