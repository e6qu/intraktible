// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/e6qu/intraktible/context-layer/command"
	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/context-layer/features"
	"github.com/e6qu/intraktible/context-layer/service"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
)

func start(t *testing.T) *testutil.API {
	t.Helper()
	log, st := testutil.NewLogStore(t)
	svc := service.New(command.NewHandler(log), st)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	return testutil.StartAPI(t, log, st, "test-key", id, svc.Routes,
		entities.Projector{}, features.Projector{}, connectors.Projector{})
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

func TestFeatureEngineEndToEnd(t *testing.T) {
	api := start(t)

	// Define two features over the customer's transaction stream.
	api.Request(t, http.MethodPost, "/v1/context/features",
		map[string]any{"name": "txn_count_24h", "entity_type": "customer", "event_name": "transaction", "aggregation": "count", "window_hours": 24},
		http.StatusAccepted, nil)
	api.Request(t, http.MethodPost, "/v1/context/features",
		map[string]any{"name": "txn_sum_24h", "entity_type": "customer", "event_name": "transaction", "aggregation": "sum", "field": "amount", "window_hours": 24},
		http.StatusAccepted, nil)

	// Two transactions just now.
	for _, amt := range []int{100, 250} {
		api.Request(t, http.MethodPost, "/v1/context/events",
			map[string]any{"entity_type": "customer", "entity_id": "c1", "event_name": "transaction", "data": map[string]any{"amount": amt}},
			http.StatusAccepted, nil)
	}

	// Compute reflects both features (async projection of the definitions + events).
	if !testutil.Eventually(t, func() bool {
		var out struct {
			Features []features.Value `json:"features"`
		}
		api.Request(t, http.MethodGet, "/v1/context/entities/customer/c1/features", nil, http.StatusOK, &out)
		got := map[string]float64{}
		for _, v := range out.Features {
			got[v.Name] = v.Value
		}
		return got["txn_count_24h"] == 2 && got["txn_sum_24h"] == 350
	}) {
		t.Fatal("features never computed to count=2 / sum=350")
	}

	// The definitions list, filtered by entity type.
	var defs struct {
		Features []features.FeatureView `json:"features"`
	}
	api.Request(t, http.MethodGet, "/v1/context/features?type=customer", nil, http.StatusOK, &defs)
	if len(defs.Features) != 2 {
		t.Fatalf("feature defs: %d, want 2", len(defs.Features))
	}
}

func TestConnectorEndToEnd(t *testing.T) {
	api := start(t)

	// Define the deterministic mock bureau connector.
	api.Request(t, http.MethodPost, "/v1/context/connectors",
		map[string]any{"name": "bureau", "type": "mock_bureau"}, http.StatusAccepted, nil)

	// Invoke it — the response is returned and recorded as an event.
	var fetched struct {
		FetchID  string `json:"fetch_id"`
		Response struct {
			Subject   string `json:"subject"`
			RiskScore int    `json:"risk_score"`
		} `json:"response"`
	}
	api.Request(t, http.MethodPost, "/v1/context/connectors/bureau/fetch",
		map[string]any{"params": map[string]any{"subject": "Acme Corp"}}, http.StatusOK, &fetched)
	if fetched.FetchID == "" || fetched.Response.Subject != "Acme Corp" {
		t.Fatalf("unexpected fetch result: %+v", fetched)
	}

	// The recorded fetch history surfaces (async projection of the event).
	if !testutil.Eventually(t, func() bool {
		var hist struct {
			Fetches []connectors.FetchView `json:"fetches"`
		}
		api.Request(t, http.MethodGet, "/v1/context/connectors/bureau/fetches", nil, http.StatusOK, &hist)
		return len(hist.Fetches) == 1 && hist.Fetches[0].Connector == "bureau"
	}) {
		t.Fatal("connector fetch was never recorded in history")
	}

	// The connector appears in the (type-filtered) listing.
	var list struct {
		Connectors []connectors.ConnectorView `json:"connectors"`
	}
	api.Request(t, http.MethodGet, "/v1/context/connectors?type=mock_bureau", nil, http.StatusOK, &list)
	if len(list.Connectors) != 1 || list.Connectors[0].Name != "bureau" {
		t.Fatalf("connector listing: %+v", list.Connectors)
	}
}

func TestConnectorCatalog(t *testing.T) {
	api := start(t)

	var cat struct {
		Templates []connectors.Template `json:"templates"`
	}
	api.Request(t, http.MethodGet, "/v1/context/connectors/catalog", nil, http.StatusOK, &cat)
	if len(cat.Templates) < 4 {
		t.Fatalf("expected a populated catalog, got %d", len(cat.Templates))
	}
	var bureau *connectors.Template
	for i := range cat.Templates {
		if cat.Templates[i].ID == "credit-bureau" {
			bureau = &cat.Templates[i]
		}
	}
	if bureau == nil || bureau.Type != "http" || len(bureau.Config) == 0 {
		t.Fatalf("credit-bureau template missing or malformed: %+v", bureau)
	}

	// A template instantiates as an ordinary connector via its scaffold config.
	api.Request(t, http.MethodPost, "/v1/context/connectors",
		map[string]any{"name": "my-bureau", "type": bureau.Type, "config": bureau.Config}, http.StatusAccepted, nil)
	if !testutil.Eventually(t, func() bool {
		var list struct {
			Connectors []connectors.ConnectorView `json:"connectors"`
		}
		api.Request(t, http.MethodGet, "/v1/context/connectors?type=http", nil, http.StatusOK, &list)
		for _, c := range list.Connectors {
			if c.Name == "my-bureau" {
				return true
			}
		}
		return false
	}) {
		t.Fatal("connector instantiated from the catalog never appeared")
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
	// Sum feature without a field -> 400.
	api.Request(t, http.MethodPost, "/v1/context/features",
		map[string]any{"name": "f", "entity_type": "customer", "event_name": "t", "aggregation": "sum", "window_hours": 24}, http.StatusBadRequest, nil)
	// Connector with an unknown type -> 400.
	api.Request(t, http.MethodPost, "/v1/context/connectors",
		map[string]any{"name": "c", "type": "carrier_pigeon"}, http.StatusBadRequest, nil)
	// Fetching an undefined connector -> 502 (the invocation could not be made).
	api.Request(t, http.MethodPost, "/v1/context/connectors/ghost/fetch", map[string]any{}, http.StatusBadGateway, nil)
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
