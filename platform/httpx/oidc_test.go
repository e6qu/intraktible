// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/httpx"
)

const (
	oidcClientID = "test-client"
	oidcNonce    = "nonce-abc"
)

// mockIdP stands up an OIDC provider: oidctest serves discovery + JWKS, and a
// /token endpoint returns an ID token signed with the test key carrying fixed
// claims (email + groups + the nonce the callback test will present).
func mockIdP(t *testing.T) *httptest.Server {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := &oidctest.Server{
		PublicKeys: []oidctest.PublicKey{{PublicKey: &priv.PublicKey, KeyID: "k1", Algorithm: oidc.RS256}},
	}
	var issuer string
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		claims := fmt.Sprintf(
			`{"iss":%q,"aud":%q,"sub":"u-1","email":"ada@acme.com","groups":["admins","staff"],"nonce":%q,"exp":%d,"iat":%d}`,
			issuer, oidcClientID, oidcNonce, time.Now().Add(time.Hour).Unix(), time.Now().Unix())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "a", "token_type": "Bearer",
			"id_token": oidctest.SignIDToken(priv, "k1", oidc.RS256, claims),
		})
	})
	mux.Handle("/", srv) // discovery + keys
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	issuer = ts.URL
	srv.SetIssuer(ts.URL)
	return ts
}

func oidcHandler(t *testing.T) (*httpx.OIDCHandler, *auth.Sessions) {
	t.Helper()
	idp := mockIdP(t)
	a, err := auth.NewOIDCAuthenticator(context.Background(), auth.OIDCConfig{
		Name: "test", Issuer: idp.URL, ClientID: oidcClientID, RedirectURL: "https://app/cb",
		Org: "demo", Workspace: "main", GroupsClaim: "groups",
		GroupRoles:  map[string]auth.Role{"admins": auth.RoleAdmin},
		DefaultRole: auth.RoleViewer,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessions()
	return httpx.NewOIDCHandler(sessions, a), sessions
}

func TestOIDCLoginRedirectsWithStateAndNonce(t *testing.T) {
	h, _ := oidcHandler(t)
	mux := http.NewServeMux()
	h.Routes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/test/login", http.NoBody))
	if rec.Code != http.StatusFound {
		t.Fatalf("login -> %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "response_type=code") || !strings.Contains(loc, "nonce=") {
		t.Fatalf("login Location lacks an auth-code request: %s", loc)
	}
	cookies := rec.Result().Cookies()
	var hasState, hasNonce bool
	for _, c := range cookies {
		hasState = hasState || c.Name == "oidc_state"
		hasNonce = hasNonce || c.Name == "oidc_nonce"
	}
	if !hasState || !hasNonce {
		t.Fatalf("login did not set state+nonce cookies: %+v", cookies)
	}

	// An unknown provider is a 404.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/nope/login", http.NoBody))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown provider login -> %d, want 404", rec.Code)
	}
}

func TestOIDCCallbackVerifiesAndIssuesSession(t *testing.T) {
	h, sessions := oidcHandler(t)
	mux := http.NewServeMux()
	h.Routes(mux)

	// Simulate the cookies login would have set; the mock token carries oidcNonce.
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/test/callback?state=s1&code=xyz", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "oidc_state", Value: "s1"})
	req.AddCookie(&http.Cookie{Name: "oidc_nonce", Value: oidcNonce})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("callback -> %d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var session string
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" && c.Value != "" {
			session = c.Value
		}
	}
	if session == "" {
		t.Fatal("callback did not issue a session cookie")
	}
	// The session maps to the OIDC identity, and the "admins" group → admin role.
	id, role, ok := sessions.Resolve(session)
	if !ok || id.Actor != "ada@acme.com" || id.Org != "demo" || role != auth.RoleAdmin {
		t.Fatalf("session resolves to %+v role=%q ok=%v", id, role, ok)
	}
}

func TestOIDCCallbackHonorsLoginGate(t *testing.T) {
	h, _ := oidcHandler(t)
	// A gate that denies the verified user (as SCIM would for a deactivated user).
	h.SetGate(func(_ context.Context, _, _, email string) bool { return email != "ada@acme.com" })
	mux := http.NewServeMux()
	h.Routes(mux)

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/test/callback?state=s1&code=xyz", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "oidc_state", Value: "s1"})
	req.AddCookie(&http.Cookie{Name: "oidc_nonce", Value: oidcNonce})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("deactivated user callback -> %d, want 403", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" && c.Value != "" {
			t.Fatal("a gated-out user must not receive a session")
		}
	}
}

func TestOIDCCallbackRejectsBadState(t *testing.T) {
	h, _ := oidcHandler(t)
	mux := http.NewServeMux()
	h.Routes(mux)

	// Cookie state and query state disagree → CSRF rejection.
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/test/callback?state=evil&code=xyz", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "oidc_state", Value: "s1"})
	req.AddCookie(&http.Cookie{Name: "oidc_nonce", Value: oidcNonce})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad-state callback -> %d, want 400", rec.Code)
	}
}
