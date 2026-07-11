// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/context-layer/command"
	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/context-layer/features"
	"github.com/e6qu/intraktible/context-layer/service"
	"github.com/e6qu/intraktible/platform/erasure"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
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

func startWithSecrets(t *testing.T, kr *connectors.Keyring) (*testutil.API, store.Store) {
	t.Helper()
	log, st := testutil.NewLogStore(t)
	svc := service.New(command.NewHandler(log), st, service.WithSecrets(kr))
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	return testutil.StartAPI(t, log, st, "test-key", id, svc.Routes,
		entities.Projector{}, features.Projector{}, connectors.Projector{}), st
}

func jsonContains(raw json.RawMessage, want string) bool {
	return strings.Contains(string(raw), want)
}

func TestContextEventErasure(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	vault := erasure.NewVault(st)
	svc := service.New(command.NewHandler(log), st, service.WithErasure(vault, []string{"ssn"}))
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	api := testutil.StartAPI(t, log, st, "test-key", id, svc.Routes, entities.Projector{}, features.Projector{})

	// A feature that sums a NON-PII field, to prove erasure doesn't break the
	// feature engine.
	api.Request(t, http.MethodPost, "/v1/context/features",
		map[string]any{"name": "spend", "entity_type": "customer", "event_name": "purchase", "aggregation": "sum", "field": "amount", "window_hours": 240000},
		http.StatusAccepted, nil)
	api.Request(t, http.MethodPost, "/v1/context/events",
		map[string]any{"entity_type": "customer", "entity_id": "ada", "event_name": "purchase",
			"data": map[string]any{"ssn": "123-45-6789", "amount": 100}},
		http.StatusAccepted, nil)

	// The read unseals the SSN for authorized callers (retry while the event
	// projection catches up).
	readSSN := func() string {
		var out struct {
			Events []struct {
				Data map[string]any `json:"data"`
			} `json:"events"`
		}
		api.Request(t, http.MethodGet, "/v1/context/entities/customer/ada/events", nil, http.StatusOK, &out)
		if len(out.Events) != 1 {
			return ""
		}
		s, _ := out.Events[0].Data["ssn"].(string)
		return s
	}
	if !testutil.Eventually(t, func() bool { return readSSN() == "123-45-6789" }) {
		t.Fatalf("event never projected with an unsealed ssn (got %q)", readSSN())
	}

	// At rest the SSN is sealed (the projected event is not plaintext).
	recs, err := st.List(context.Background(), entities.CollectionEvents, "")
	if err != nil {
		t.Fatal(err)
	}
	var anyDoc string
	for _, r := range recs {
		anyDoc += string(r.Doc)
	}
	if strings.Contains(anyDoc, "123-45-6789") || !strings.Contains(anyDoc, "$intraktible_erased") {
		t.Fatalf("event SSN not sealed at rest: %s", anyDoc)
	}

	// Erase the subject (crypto-shred). The SSN is now permanently "[erased]".
	if err := vault.Erase(context.Background(), id, "customer/ada"); err != nil {
		t.Fatal(err)
	}
	if got := readSSN(); got != "[erased]" {
		t.Fatalf("ssn after erasure = %q, want [erased]", got)
	}

	// The feature over the non-PII "amount" still computes — erasure did not
	// touch it (retry while the feature definition projects).
	if !testutil.Eventually(t, func() bool {
		var out struct {
			Features []features.Value `json:"features"`
		}
		api.Request(t, http.MethodGet, "/v1/context/entities/customer/ada/features", nil, http.StatusOK, &out)
		for _, v := range out.Features {
			if v.Name == "spend" {
				return v.Value == 100
			}
		}
		return false
	}) {
		t.Fatal("the non-PII 'spend' feature should still compute to 100 after erasure")
	}
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

	// The definitions list, filtered by entity type — versioned.
	var defs struct {
		Features []features.FeatureView `json:"features"`
	}
	api.Request(t, http.MethodGet, "/v1/context/features?type=customer", nil, http.StatusOK, &defs)
	if len(defs.Features) != 2 {
		t.Fatalf("feature defs: %d, want 2", len(defs.Features))
	}
	for _, f := range defs.Features {
		if f.Version != 1 {
			t.Fatalf("feature %q version = %d, want 1", f.Name, f.Version)
		}
	}

	// The wider aggregation set: max and count_distinct over the same stream.
	api.Request(t, http.MethodPost, "/v1/context/features",
		map[string]any{"name": "txn_max_24h", "entity_type": "customer", "event_name": "transaction", "aggregation": "max", "field": "amount", "window_hours": 24},
		http.StatusAccepted, nil)
	if !testutil.Eventually(t, func() bool {
		var out struct {
			Features []features.Value `json:"features"`
		}
		api.Request(t, http.MethodGet, "/v1/context/entities/customer/c1/features", nil, http.StatusOK, &out)
		for _, v := range out.Features {
			if v.Name == "txn_max_24h" {
				return v.Value == 250
			}
		}
		return false
	}) {
		t.Fatal("max feature never computed to 250")
	}

	// Point-in-time: as of the far past (before any event), the count is 0 — the
	// feature is reproducible for a historical instant.
	var past struct {
		Features []features.Value `json:"features"`
	}
	api.Request(t, http.MethodGet, "/v1/context/entities/customer/c1/features?as_of=2000-01-01T00:00:00Z", nil, http.StatusOK, &past)
	for _, v := range past.Features {
		if v.Name == "txn_count_24h" && v.Value != 0 {
			t.Fatalf("as-of-2000 count = %v, want 0 (no events had occurred)", v.Value)
		}
	}
	// A malformed as_of is rejected.
	api.Request(t, http.MethodGet, "/v1/context/entities/customer/c1/features?as_of=nonsense", nil, http.StatusBadRequest, nil)
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

func TestConnectorDefinitionEncryptsStoredSecrets(t *testing.T) {
	kr, err := connectors.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	api, st := startWithSecrets(t, kr)

	api.Request(t, http.MethodPost, "/v1/context/connectors",
		map[string]any{
			"name": "secure-sql",
			"type": "sql",
			"config": map[string]any{
				"dsn":   "file:plaintext-secret.db",
				"query": "SELECT 1",
			},
		}, http.StatusAccepted, nil)

	var stored connectors.ConnectorView
	if !testutil.Eventually(t, func() bool {
		var ok bool
		stored, ok, err = store.GetDoc[connectors.ConnectorView](
			t.Context(), st, connectors.CollectionConnectors, store.Key("demo", "main", "secure-sql"))
		return err == nil && ok
	}) {
		t.Fatalf("connector projection missing or errored: %v", err)
	}
	if string(stored.Config) == "" || !json.Valid(stored.Config) {
		t.Fatalf("stored config invalid: %s", stored.Config)
	}
	if string(stored.Config) == `{"dsn":"file:plaintext-secret.db","query":"SELECT 1"}` ||
		jsonContains(stored.Config, "plaintext-secret") {
		t.Fatalf("stored config leaked plaintext secret: %s", stored.Config)
	}

	var list struct {
		Connectors []connectors.ConnectorView `json:"connectors"`
	}
	api.Request(t, http.MethodGet, "/v1/context/connectors?type=sql", nil, http.StatusOK, &list)
	if len(list.Connectors) != 1 {
		t.Fatalf("connector listing: %+v", list.Connectors)
	}
	if !jsonContains(list.Connectors[0].Config, "[redacted]") || jsonContains(list.Connectors[0].Config, "plaintext-secret") {
		t.Fatalf("list response did not redact secret: %s", list.Connectors[0].Config)
	}
}

// TestConnectorDefinitionRedactsSecretsWithoutKeyring is the fail-safe-default
// guard: with NO keyring configured, a connector's credential fields must be
// redacted in the recorded ConnectorDefined event (and so in the tenant-readable
// audit surface), never persisted in plaintext.
func TestConnectorDefinitionRedactsSecretsWithoutKeyring(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	svc := service.New(command.NewHandler(log), st) // no WithSecrets — the default
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	api := testutil.StartAPI(t, log, st, "test-key", id, svc.Routes,
		entities.Projector{}, features.Projector{}, connectors.Projector{})

	const secret = "super-secret-token-value"
	api.Request(t, http.MethodPost, "/v1/context/connectors",
		map[string]any{
			"name": "leaky-http",
			"type": "http",
			"config": map[string]any{
				"url": "https://example.test/api",
				"headers": map[string]any{
					"authorization": "Bearer " + secret,
				},
			},
		}, http.StatusAccepted, nil)

	evs, err := log.Read(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawDefined bool
	for _, e := range evs {
		if e.Type != "context.connector_defined" {
			continue
		}
		sawDefined = true
		if strings.Contains(string(e.Payload), secret) {
			t.Fatalf("ConnectorDefined event leaked plaintext secret: %s", e.Payload)
		}
		if !strings.Contains(string(e.Payload), "[redacted]") {
			t.Fatalf("ConnectorDefined event did not redact the credential: %s", e.Payload)
		}
	}
	if !sawDefined {
		t.Fatal("no ConnectorDefined event recorded")
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
