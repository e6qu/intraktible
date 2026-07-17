// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
)

func TestHealth(t *testing.T) {
	// Healthy: check returns nil -> 200 ok.
	ok := httptest.NewRecorder()
	httpx.Health(func() error { return nil })(ok, httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody))
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), `"ok"`) {
		t.Fatalf("healthy -> %d body=%s", ok.Code, ok.Body.String())
	}
	// A stalled projection -> 503 degraded with the error surfaced.
	bad := httptest.NewRecorder()
	httpx.Health(func() error { return errors.New("projector boom") })(bad, httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody))
	if bad.Code != http.StatusServiceUnavailable || !strings.Contains(bad.Body.String(), "projector boom") {
		t.Fatalf("degraded -> %d body=%s", bad.Code, bad.Body.String())
	}
	// A nil check is treated as healthy.
	nilc := httptest.NewRecorder()
	httpx.Health(nil)(nilc, httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody))
	if nilc.Code != http.StatusOK {
		t.Fatalf("nil check -> %d, want 200", nilc.Code)
	}
}

func TestLoginLogoutFlow(t *testing.T) {
	keyring := auth.NewKeyring()
	sessions := auth.NewSessions()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	keyring.Add("good-key", auth.APIKey{ID: "k", Identity: id, Scope: auth.Sandbox})

	login := httpx.LoginHandler(keyring, sessions)

	// A bad key is rejected.
	bad := httptest.NewRecorder()
	login(bad, httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"api_key":"nope"}`)))
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("bad key -> %d, want 401", bad.Code)
	}

	// A good key issues a session cookie that the Authenticate middleware accepts.
	rec := httptest.NewRecorder()
	login(rec, httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"api_key":"good-key"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("login -> %d, want 200", rec.Code)
	}
	cookies := rec.Result().Cookies()
	var token string
	for _, c := range cookies {
		if c.Name == "session" && c.HttpOnly {
			token = c.Value
		}
	}
	if token == "" {
		t.Fatal("login did not set an HttpOnly session cookie")
	}
	if gotID, _, _, ok := sessions.Resolve(token); !ok || gotID != id {
		t.Fatalf("issued session did not resolve to the key's identity: %v %v", gotID, ok)
	}

	// Logout revokes the session and clears the cookie.
	out := httptest.NewRecorder()
	logoutReq := httptest.NewRequest(http.MethodPost, "/v1/logout", http.NoBody)
	logoutReq.AddCookie(&http.Cookie{Name: "session", Value: token})
	httpx.LogoutHandler(sessions)(out, logoutReq)
	if out.Code != http.StatusOK {
		t.Fatalf("logout -> %d, want 200", out.Code)
	}
	var logout struct {
		URL string `json:"logout_url"`
	}
	if err := json.NewDecoder(out.Body).Decode(&logout); err != nil {
		t.Fatalf("decode logout response: %v", err)
	}
	if logout.URL != "" {
		t.Fatalf("api-key logout URL = %q, want empty", logout.URL)
	}
	if _, _, _, ok := sessions.Resolve(token); ok {
		t.Fatal("session should be revoked after logout")
	}
}

func TestLogoutReturnsSessionBoundOIDCFrontChannelURL(t *testing.T) {
	sessions := auth.NewSessions()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	const providerLogoutURL = "https://auth.example.test/oauth2/sessions/logout"
	token, err := sessions.IssueSSO(id, auth.RoleViewer, auth.ScopeAll, providerLogoutURL)
	if err != nil {
		t.Fatalf("issue SSO session: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/logout?redirect=https://attacker.example", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	httpx.LogoutHandler(sessions)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout -> %d, want 200", rec.Code)
	}
	var logout struct {
		URL string `json:"logout_url"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&logout); err != nil {
		t.Fatalf("decode logout response: %v", err)
	}
	if logout.URL != providerLogoutURL {
		t.Fatalf("logout URL = %q, want %q", logout.URL, providerLogoutURL)
	}
	if _, _, _, ok := sessions.Resolve(token); ok {
		t.Fatal("SSO session should be revoked after logout")
	}
}

func TestMeHandler(t *testing.T) {
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	me := httpx.MeHandler()

	// Without an identity in context -> 401.
	un := httptest.NewRecorder()
	me(un, httptest.NewRequest(http.MethodGet, "/v1/me", http.NoBody))
	if un.Code != http.StatusUnauthorized {
		t.Fatalf("me without identity -> %d, want 401", un.Code)
	}

	// With an identity (as the auth middleware would set) -> 200 + the identity.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/me", http.NoBody)
	me(rec, req.WithContext(identity.With(req.Context(), id)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"actor":"dev"`) {
		t.Fatalf("me -> %d body=%s", rec.Code, rec.Body.String())
	}
}
