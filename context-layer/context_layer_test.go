// SPDX-License-Identifier: AGPL-3.0-or-later

package contextlayer_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/intraktible/context-layer/command"
	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/context-layer/features"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

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
