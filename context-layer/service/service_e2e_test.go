// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/e6qu/intraktible/context-layer/command"
	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/context-layer/service"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
)

func start(t *testing.T) *testutil.API {
	t.Helper()
	log, st := testutil.NewLogStore(t)
	svc := service.New(command.NewHandler(log), st)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	return testutil.StartAPI(t, log, st, "test-key", id, svc.Routes, entities.Projector{})
}

func TestContextAPIEndToEnd(t *testing.T) {
	api := start(t)

	// Record then patch an entity.
	api.Request(t, http.MethodPost, "/v1/context/entities",
		map[string]any{"entity_type": "customer", "entity_id": "c1", "attributes": map[string]any{"tier": "silver", "country": "US"}},
		http.StatusAccepted, nil)
	api.Request(t, http.MethodPost, "/v1/context/entities",
		map[string]any{"entity_type": "customer", "entity_id": "c1", "attributes": map[string]any{"tier": "gold", "kyc": true}},
		http.StatusAccepted, nil)

	// Record two events about it.
	for _, amt := range []int{100, 250} {
		api.Request(t, http.MethodPost, "/v1/context/events",
			map[string]any{"entity_type": "customer", "entity_id": "c1", "event_name": "transaction", "data": map[string]any{"amount": amt}},
			http.StatusAccepted, nil)
	}

	// The entity reflects the merge + event count (async projection).
	if !testutil.Eventually(t, func() bool {
		var c entities.EntityView
		api.Request(t, http.MethodGet, "/v1/context/entities/customer/c1", nil, http.StatusOK, &c)
		var attrs map[string]any
		if err := json.Unmarshal(c.Attributes, &attrs); err != nil {
			return false
		}
		return attrs["tier"] == "gold" && attrs["country"] == "US" && attrs["kyc"] == true && c.EventCount == 2
	}) {
		t.Fatal("entity never reflected the merge + events")
	}

	// Per-entity event log, newest first.
	var events struct {
		Events []entities.EventView `json:"events"`
	}
	api.Request(t, http.MethodGet, "/v1/context/entities/customer/c1/events", nil, http.StatusOK, &events)
	if len(events.Events) != 2 {
		t.Fatalf("events: %d, want 2", len(events.Events))
	}

	// Type filter on the listing.
	var list struct {
		Entities []entities.EntityView `json:"entities"`
	}
	api.Request(t, http.MethodGet, "/v1/context/entities?type=customer", nil, http.StatusOK, &list)
	if len(list.Entities) != 1 {
		t.Fatalf("customers: %d, want 1", len(list.Entities))
	}
	api.Request(t, http.MethodGet, "/v1/context/entities?type=merchant", nil, http.StatusOK, &list)
	if len(list.Entities) != 0 {
		t.Fatalf("merchants: %d, want 0", len(list.Entities))
	}
}

func TestContextAPIValidationAndAuth(t *testing.T) {
	api := start(t)

	// Missing entity_type -> 400.
	api.Request(t, http.MethodPost, "/v1/context/entities", map[string]any{"entity_id": "c1"}, http.StatusBadRequest, nil)
	// Non-object attributes -> 400.
	api.Request(t, http.MethodPost, "/v1/context/entities",
		map[string]any{"entity_type": "customer", "entity_id": "c1", "attributes": []int{1, 2}}, http.StatusBadRequest, nil)
	// Event without a name -> 400.
	api.Request(t, http.MethodPost, "/v1/context/events", map[string]any{"entity_type": "customer", "entity_id": "c1"}, http.StatusBadRequest, nil)
	// Unknown entity -> 404.
	api.Request(t, http.MethodGet, "/v1/context/entities/customer/ghost", nil, http.StatusNotFound, nil)
	// Unauthenticated -> 401.
	resp, err := http.Get(api.Server.URL + "/v1/context/entities")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated -> %d, want 401", resp.StatusCode)
	}
}
