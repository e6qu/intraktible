// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

func define(ctx context.Context, t *testing.T, s store.Store, id identity.Identity, v connectors.ConnectorView) {
	t.Helper()
	v.Org, v.Workspace = id.Org, id.Workspace
	if err := store.PutDoc(ctx, s, connectors.CollectionConnectors, store.Key(id.Org, id.Workspace, v.Name), v); err != nil {
		t.Fatal(err)
	}
}

func TestMockBureauDeterministic(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	define(ctx, t, s, id, connectors.ConnectorView{Name: "bureau", Type: domain.ConnectorMockBureau})

	params := json.RawMessage(`{"subject":"Acme Corp"}`)
	r1, err := connectors.Invoke(ctx, s, id, "bureau", params)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := connectors.Invoke(ctx, s, id, "bureau", params)
	if err != nil {
		t.Fatal(err)
	}
	if string(r1) != string(r2) {
		t.Fatalf("mock bureau not deterministic: %s vs %s", r1, r2)
	}
	var out struct {
		Subject   string `json:"subject"`
		RiskScore int    `json:"risk_score"`
	}
	if err := json.Unmarshal(r1, &out); err != nil {
		t.Fatal(err)
	}
	if out.Subject != "Acme Corp" || out.RiskScore < 0 || out.RiskScore > 100 {
		t.Fatalf("unexpected bureau response: %s", r1)
	}
}

func TestHTTPConnector(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"score":42}`))
	}))
	defer srv.Close()

	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	define(ctx, t, s, id, connectors.ConnectorView{
		Name: "rest", Type: domain.ConnectorHTTP, Config: json.RawMessage(`{"url":"` + srv.URL + `"}`),
	})

	resp, err := connectors.Invoke(ctx, s, id, "rest", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		OK    bool `json:"ok"`
		Score int  `json:"score"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Score != 42 {
		t.Fatalf("http connector response: %s", resp)
	}
}

func TestHTTPConnectorNon2xxFailsLoudly(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	define(ctx, t, s, id, connectors.ConnectorView{
		Name: "rest", Type: domain.ConnectorHTTP, Config: json.RawMessage(`{"url":"` + srv.URL + `"}`),
	})
	if _, err := connectors.Invoke(ctx, s, id, "rest", nil); err == nil {
		t.Fatal("expected a non-2xx fetch to error")
	}
}

func TestInvokeUnknownConnector(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	if _, err := connectors.Invoke(ctx, s, id, "ghost", nil); err == nil {
		t.Fatal("expected error for unknown connector")
	}
}
