// SPDX-License-Identifier: AGPL-3.0-or-later

package contextlayer_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/context-layer/command"
	"github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/context-layer/entities"
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
