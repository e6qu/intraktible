// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

func TestHTTPConnectorAuthAndHeaders(t *testing.T) {
	ctx := context.Background()
	var gotAuth, gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotHeader = r.Header.Get("X-Tenant")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	define(ctx, t, s, id, connectors.ConnectorView{
		Name: "rest", Type: domain.ConnectorHTTP,
		Config: json.RawMessage(`{"url":"` + srv.URL + `","method":"POST","auth":{"type":"bearer","token":"tok-abc"},"headers":{"X-Tenant":"acme"}}`),
	})

	if _, err := connectors.InvokeWith(ctx, s, id, "rest", json.RawMessage(`{"x":1}`), connectors.EgressPolicy{AllowPrivate: true}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotHeader != "acme" {
		t.Fatalf("X-Tenant = %q", gotHeader)
	}
}

func TestGraphQLConnectorAuth(t *testing.T) {
	ctx := context.Background()
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer srv.Close()

	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	define(ctx, t, s, id, connectors.ConnectorView{
		Name: "gql", Type: domain.ConnectorGraphQL,
		Config: json.RawMessage(`{"url":"` + srv.URL + `","query":"{ ok }","auth":{"type":"header","name":"X-Api-Key","value":"k-1"}}`),
	})

	if _, err := connectors.InvokeWith(ctx, s, id, "gql", nil, connectors.EgressPolicy{AllowPrivate: true}); err != nil {
		t.Fatal(err)
	}
	if gotKey != "k-1" {
		t.Fatalf("X-Api-Key = %q", gotKey)
	}
}

func TestHTTPConnectorRejectsBadAuth(t *testing.T) {
	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	define(context.Background(), t, s, id, connectors.ConnectorView{
		Name: "bad", Type: domain.ConnectorHTTP,
		Config: json.RawMessage(`{"url":"https://api.example.com/x","auth":{"type":"bearer"}}`),
	})
	if _, err := connectors.InvokeWith(context.Background(), s, id, "bad", nil, connectors.EgressPolicy{}); err == nil {
		t.Fatal("a bearer auth with no token should fail to build")
	}
}

// The auth block is a recognized credential field, so it is sealed at rest and
// masked in the redacted view — yet decrypts transparently for the actual fetch.
func TestAuthBlockSealedAtRest(t *testing.T) {
	ctx := context.Background()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	kr, err := connectors.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := json.RawMessage(`{"url":"` + srv.URL + `","method":"POST","auth":{"type":"bearer","token":"super-secret"}}`)
	enc, err := connectors.EncryptSecrets(cfg, kr, loc("rest"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enc), "super-secret") {
		t.Fatalf("encrypted config leaked the token: %s", enc)
	}

	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	view := connectors.ConnectorView{Name: "rest", Type: domain.ConnectorHTTP, Config: enc}
	define(ctx, t, s, id, view)

	if strings.Contains(string(view.Redacted().Config), "super-secret") {
		t.Fatalf("redacted view leaked the token: %s", view.Redacted().Config)
	}

	if _, err := connectors.InvokeWithSecrets(ctx, s, id, "rest", json.RawMessage(`{"x":1}`),
		connectors.EgressPolicy{AllowPrivate: true}, kr); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer super-secret" {
		t.Fatalf("decrypted auth not applied: %q", gotAuth)
	}
}
