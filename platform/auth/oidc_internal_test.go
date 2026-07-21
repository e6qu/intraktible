// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"
	"golang.org/x/oauth2"
)

func TestDiscoveredAuthStyle(t *testing.T) {
	for _, test := range []struct {
		name    string
		methods []string
		want    oauth2.AuthStyle
	}{
		{name: "Shauth client secret post", methods: []string{"client_secret_post"}, want: oauth2.AuthStyleInParams},
		{name: "HTTP Basic", methods: []string{"client_secret_basic"}, want: oauth2.AuthStyleInHeader},
		{name: "post preferred when both are advertised", methods: []string{"client_secret_basic", "client_secret_post"}, want: oauth2.AuthStyleInParams},
		{name: "provider discovery omitted methods", want: oauth2.AuthStyleAutoDetect},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := discoveredAuthStyle(test.methods); got != test.want {
				t.Fatalf("discoveredAuthStyle(%v) = %v, want %v", test.methods, got, test.want)
			}
		})
	}
}

func TestAbsoluteOIDCURLAllowsOnlyHTTPTransport(t *testing.T) {
	for _, raw := range []string{"ftp://identity.example.test/path", "file://identity.example.test/path", "javascript://identity.example.test/path"} {
		if _, err := absoluteURL(raw); err == nil {
			t.Fatalf("absoluteURL(%q) accepted a non-HTTP transport", raw)
		}
	}
	for _, raw := range []string{"https://identity.example.test/path", "http://localhost:8080/path"} {
		if _, err := absoluteURL(raw); err != nil {
			t.Fatalf("absoluteURL(%q) = %v", raw, err)
		}
	}
}

func TestOIDCDiscoveryIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	started := time.Now()
	_, err := newOIDCAuthenticator(context.Background(), OIDCConfig{
		Name: "shauth", Issuer: server.URL, ClientID: "intraktible",
		RedirectURL: server.URL + "/v1/auth/oidc/shauth/callback",
		Org:         "e6qu", Workspace: "dev",
	}, 30*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "discovery") {
		t.Fatalf("bounded discovery error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded discovery took %s", elapsed)
	}
}

func TestOIDCDiscoveryMetadataIsCachedByAuthenticator(t *testing.T) {
	var discoveryRequests atomic.Int32
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		discoveryRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"issuer": serverURL, "authorization_endpoint": serverURL + "/authorize",
			"token_endpoint": serverURL + "/token", "jwks_uri": serverURL + "/keys",
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	serverURL = server.URL

	authenticator, err := NewOIDCAuthenticator(context.Background(), OIDCConfig{
		Name: "shauth", Issuer: server.URL, ClientID: "intraktible",
		RedirectURL: server.URL + "/v1/auth/oidc/shauth/callback",
		Org:         "e6qu", Workspace: "dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if target := authenticator.AuthCodeURL("state", "nonce", "verifier"); !strings.HasPrefix(target, server.URL+"/authorize?") {
			t.Fatalf("authorization URL = %q", target)
		}
	}
	if got := discoveryRequests.Load(); got != 1 {
		t.Fatalf("discovery requests = %d, want exactly one startup request", got)
	}
}

func TestOIDCTokenExchangeIsBounded(t *testing.T) {
	var serverURL string
	releaseTokenEndpoint := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"issuer": serverURL, "authorization_endpoint": serverURL + "/authorize",
				"token_endpoint": serverURL + "/token", "jwks_uri": serverURL + "/keys",
				"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
				"id_token_signing_alg_values_supported": []string{"RS256"},
				"token_endpoint_auth_methods_supported": []string{"client_secret_post"},
			}); err != nil {
				t.Error(err)
			}
		case "/token":
			<-releaseTokenEndpoint
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(releaseTokenEndpoint) })
	serverURL = server.URL

	authenticator, err := NewOIDCAuthenticator(context.Background(), OIDCConfig{
		Name: "shauth", Issuer: server.URL, ClientID: "intraktible", ClientSecret: "secret",
		RedirectURL: server.URL + "/v1/auth/oidc/shauth/callback",
		Org:         "e6qu", Workspace: "dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = authenticator.exchange(context.Background(), "code", "nonce", "verifier", 30*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "token exchange") {
		t.Fatalf("bounded token exchange error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded token exchange took %s", elapsed)
	}
}

type verifierTestProvider struct {
	server      *httptest.Server
	issuer      string
	privateKey  *rsa.PrivateKey
	keyRequests atomic.Int32
	stallFirst  atomic.Bool
	keys        *oidctest.Server
}

func newVerifierTestProvider(t *testing.T, stallFirst bool) *verifierTestProvider {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	provider := &verifierTestProvider{
		privateKey: privateKey,
		keys: &oidctest.Server{PublicKeys: []oidctest.PublicKey{{
			PublicKey: &privateKey.PublicKey, KeyID: "known", Algorithm: oidc.RS256,
		}}},
	}
	provider.stallFirst.Store(stallFirst)
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"issuer": provider.issuer, "authorization_endpoint": provider.issuer + "/authorize",
			"token_endpoint": provider.issuer + "/token", "jwks_uri": provider.issuer + "/keys",
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
			"id_token_signing_alg_values_supported": []string{oidc.RS256},
		}); err != nil {
			t.Error(err)
		}
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		provider.keyRequests.Add(1)
		if provider.stallFirst.CompareAndSwap(true, false) {
			<-r.Context().Done()
			return
		}
		provider.keys.ServeHTTP(w, r)
	})
	provider.server = httptest.NewServer(mux)
	t.Cleanup(provider.server.Close)
	provider.issuer = provider.server.URL
	provider.keys.SetIssuer(provider.issuer)
	return provider
}

func (provider *verifierTestProvider) token(keyID, jti string) string {
	claims := fmt.Sprintf(
		`{"iss":%q,"aud":"intraktible","sub":"user-1","sid":"session-1","jti":%q,"iat":%d,"exp":%d,"events":{%q:{}}}`,
		provider.issuer, jti, time.Now().Unix(), time.Now().Add(time.Minute).Unix(), backChannelLogoutEvent,
	)
	return oidctest.SignIDToken(provider.privateKey, keyID, oidc.RS256, claims)
}

func newVerifierAuthenticator(t *testing.T, provider *verifierTestProvider, timeout time.Duration) *OIDCAuthenticator {
	t.Helper()
	authenticator, err := newOIDCAuthenticator(context.Background(), OIDCConfig{
		Name: "shauth", Issuer: provider.issuer, ClientID: "intraktible",
		RedirectURL: provider.issuer + "/v1/auth/oidc/shauth/callback",
		Org:         "e6qu", Workspace: "dev",
	}, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return authenticator
}

func TestOIDCJWKSRefreshIsBoundedAndRecovers(t *testing.T) {
	provider := newVerifierTestProvider(t, true)
	authenticator := newVerifierAuthenticator(t, provider, 40*time.Millisecond)
	authenticator.logoutVerifyCooldown = 0

	started := time.Now()
	if _, err := authenticator.VerifyLogoutToken(context.Background(), provider.token("known", "stalled")); err == nil {
		t.Fatal("stalled JWKS verification succeeded")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled JWKS verification took %s", elapsed)
	}
	if _, err := authenticator.VerifyLogoutToken(context.Background(), provider.token("known", "recovered")); err != nil {
		t.Fatalf("verification did not recover after the JWKS endpoint recovered: %v", err)
	}
	if got := provider.keyRequests.Load(); got != 2 {
		t.Fatalf("JWKS requests = %d, want one bounded failure and one recovery", got)
	}
}

func TestOIDCUnknownKeyBurstIsThrottled(t *testing.T) {
	provider := newVerifierTestProvider(t, false)
	authenticator := newVerifierAuthenticator(t, provider, time.Second)
	authenticator.logoutVerifyCooldown = time.Hour

	for index := range 12 {
		if _, err := authenticator.VerifyLogoutToken(context.Background(), provider.token(fmt.Sprintf("unknown-%d", index), fmt.Sprintf("unknown-%d", index))); err == nil {
			t.Fatalf("unknown key %d verified", index)
		}
	}
	if got := provider.keyRequests.Load(); got != 1 {
		t.Fatalf("unknown-key burst made %d JWKS requests, want exactly one", got)
	}

	authenticator.logoutVerifyNotBefore = time.Time{}
	if _, err := authenticator.VerifyLogoutToken(context.Background(), provider.token("known", "valid")); err != nil {
		t.Fatalf("valid token was rejected after the failure circuit reopened: %v", err)
	}
	if got := provider.keyRequests.Load(); got != 1 {
		t.Fatalf("valid cached key triggered an extra JWKS request: %d", got)
	}
}

func TestOIDCRejectsForeignEndSessionOrigin(t *testing.T) {
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
			"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys",
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
			"id_token_signing_alg_values_supported": []string{oidc.RS256},
			"end_session_endpoint":                  "https://attacker.example/logout",
		})
	}))
	t.Cleanup(server.Close)
	issuer = server.URL

	_, err := NewOIDCAuthenticator(context.Background(), OIDCConfig{
		Name: "shauth", Issuer: issuer, ClientID: "intraktible",
		RedirectURL:           issuer + "/v1/auth/oidc/shauth/callback",
		PostLogoutRedirectURL: issuer + "/auth/shauth/logout/complete",
		Org:                   "e6qu", Workspace: "dev",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid end-session endpoint") {
		t.Fatalf("foreign end-session endpoint error = %v", err)
	}
}
