// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The provider adapters hardcode the upstream host (sandbox.plaid.com, etc.), so
// these white-box tests construct the connector against an httptest server to
// exercise request building + response handling without real network calls.

func TestPlaidConnectorInjectsCredentials(t *testing.T) {
	ctx := context.Background()
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		if r.URL.Path != "/accounts/balance/get" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accounts":[{"balance":1234}]}`))
	}))
	defer srv.Close()

	c := plaidConnector{
		baseURL: srv.URL, clientID: "cid-123", secret: "sek-xyz",
		path: "/accounts/balance/get", client: srv.Client(),
	}
	resp, err := c.Fetch(ctx, json.RawMessage(`{"access_token":"acc-tok"}`))
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["client_id"] != "cid-123" || gotBody["secret"] != "sek-xyz" {
		t.Fatalf("credentials not injected into body: %v", gotBody)
	}
	if gotBody["access_token"] != "acc-tok" {
		t.Fatalf("params not forwarded: %v", gotBody)
	}
	if !json.Valid(resp) {
		t.Fatalf("response not JSON: %s", resp)
	}
}

func TestStripeConnectorBearerAndQuery(t *testing.T) {
	ctx := context.Background()
	var gotAuth, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer srv.Close()

	c := stripeConnector{baseURL: srv.URL, secretKey: "sk_test_123", path: "/v1/charges", client: srv.Client()}
	if _, err := c.Fetch(ctx, json.RawMessage(`{"limit":3}`)); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk_test_123" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotQuery != "3" {
		t.Fatalf("limit query = %q, want 3", gotQuery)
	}
}

func TestProviderConfigValidation(t *testing.T) {
	egress := EgressPolicy{}
	cases := []struct {
		name   string
		build  func() error
		wantOK bool
	}{
		{"plaid ok", func() error {
			_, e := newPlaid(json.RawMessage(`{"env":"sandbox","client_id":"c","secret":"s","path":"/x"}`), egress)
			return e
		}, true},
		{"plaid bad env", func() error {
			_, e := newPlaid(json.RawMessage(`{"env":"prod","client_id":"c","secret":"s","path":"/x"}`), egress)
			return e
		}, false},
		{"plaid no creds", func() error { _, e := newPlaid(json.RawMessage(`{"env":"sandbox","path":"/x"}`), egress); return e }, false},
		{"plaid bad path", func() error {
			_, e := newPlaid(json.RawMessage(`{"env":"sandbox","client_id":"c","secret":"s","path":"x"}`), egress)
			return e
		}, false},
		{"stripe ok", func() error {
			_, e := newStripe(json.RawMessage(`{"secret_key":"sk","path":"/v1/x"}`), egress)
			return e
		}, true},
		{"stripe no key", func() error { _, e := newStripe(json.RawMessage(`{"path":"/v1/x"}`), egress); return e }, false},
		{"stripe bad path", func() error {
			_, e := newStripe(json.RawMessage(`{"secret_key":"sk","path":"v1/x"}`), egress)
			return e
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.build()
			if c.wantOK && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !c.wantOK && err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestOAuth2ClientCredentials(t *testing.T) {
	ctx := context.Background()
	var tokenHits int
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenHits++
		_ = r.ParseForm()
		if r.FormValue("grant_type") != "client_credentials" || r.FormValue("client_id") != "cid" || r.FormValue("client_secret") != "sek" {
			t.Errorf("unexpected token form: %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok-123","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer tokenSrv.Close()

	auth := &authConfig{Type: "oauth2", TokenURL: tokenSrv.URL, ClientID: "cid", ClientSecret: "sek"}
	if err := auth.validate(); err != nil {
		t.Fatal(err)
	}

	authorize := func() string {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example.com/x", http.NoBody)
		if err := auth.authorize(ctx, req, tokenSrv.Client()); err != nil {
			t.Fatal(err)
		}
		return req.Header.Get("Authorization")
	}

	if got := authorize(); got != "Bearer tok-123" {
		t.Fatalf("authorization = %q", got)
	}
	// Second call reuses the cached token (no second token-endpoint hit).
	if got := authorize(); got != "Bearer tok-123" {
		t.Fatalf("authorization (cached) = %q", got)
	}
	if tokenHits != 1 {
		t.Fatalf("token endpoint hit %d times, want 1 (cached)", tokenHits)
	}
}

// A rotated client_secret (same token_url/client_id/scope) must trigger a fresh
// token fetch, not serve the token cached under the old secret.
func TestOAuth2RotatedSecretRefetches(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		// Echo the secret into the token so the test can tell them apart.
		_, _ = w.Write([]byte(`{"access_token":"tok-` + r.FormValue("client_secret") + `","expires_in":3600}`))
	}))
	defer srv.Close()

	get := func(secret string) string {
		a := &authConfig{Type: "oauth2", TokenURL: srv.URL, ClientID: "cid", ClientSecret: secret}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example.com/x", http.NoBody)
		if err := a.authorize(ctx, req, srv.Client()); err != nil {
			t.Fatal(err)
		}
		return req.Header.Get("Authorization")
	}
	if got := get("s1"); got != "Bearer tok-s1" {
		t.Fatalf("first secret: %q", got)
	}
	if got := get("s2"); got != "Bearer tok-s2" {
		t.Fatalf("rotated secret served a stale token: %q (want Bearer tok-s2)", got)
	}
}

func TestOAuth2Validation(t *testing.T) {
	bad := []*authConfig{
		{Type: "oauth2", ClientID: "c", ClientSecret: "s"},                        // no token_url
		{Type: "oauth2", TokenURL: "not-a-url", ClientID: "c", ClientSecret: "s"}, // bad url
		{Type: "oauth2", TokenURL: "https://idp/token", ClientSecret: "s"},        // no client_id
		{Type: "oauth2", TokenURL: "https://idp/token", ClientID: "c"},            // no client_secret
	}
	for _, a := range bad {
		if err := a.validate(); err == nil {
			t.Fatalf("%+v should be invalid", a)
		}
	}
}

func TestAuthConfigValidate(t *testing.T) {
	ok := []*authConfig{
		nil,
		{Type: ""},
		{Type: "bearer", Token: "t"},
		{Type: "header", Name: "X-Api-Key", Value: "v"},
		{Type: "basic", Username: "u", Password: "p"},
		{Type: "query", Name: "key", Value: "v"},
	}
	for _, a := range ok {
		if err := a.validate(); err != nil {
			t.Fatalf("%+v should be valid: %v", a, err)
		}
	}
	bad := []*authConfig{
		{Type: "bearer"},
		{Type: "header"},
		{Type: "basic"},
		{Type: "nonsense"},
	}
	for _, a := range bad {
		if err := a.validate(); err == nil {
			t.Fatalf("%+v should be invalid", a)
		}
	}
}
