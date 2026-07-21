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
	setSameOriginLogoutHeaders(logoutReq, "http://example.com")
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
	sso := auth.SSOSession{Protocol: "oidc", Provider: "shauth", Issuer: "https://auth.example.test", Subject: "subject", SID: "sid", IDToken: "signed.id.token", ClientID: "intraktible", EndSessionEndpoint: providerLogoutURL, PostLogoutRedirectURL: "https://intraktible.example.test/auth/shauth/logout/complete"}
	token, err := sessions.IssueSSO(id, auth.RoleViewer, auth.ScopeAll, sso)
	if err != nil {
		t.Fatalf("issue SSO session: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/logout?redirect=https://attacker.example", http.NoBody)
	setSameOriginLogoutHeaders(req, "http://example.com")
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
	wantURL := providerLogoutURL + "?client_id=intraktible&id_token_hint=signed.id.token&post_logout_redirect_uri=https%3A%2F%2Fintraktible.example.test%2Fauth%2Fshauth%2Flogout%2Fcomplete"
	if logout.URL != wantURL {
		t.Fatalf("logout URL = %q, want %q", logout.URL, wantURL)
	}
	if _, _, _, ok := sessions.Resolve(token); ok {
		t.Fatal("SSO session should be revoked after logout")
	}
}

func TestLogoutRejectsCrossOriginWithoutRevokingSession(t *testing.T) {
	for _, tc := range []struct {
		name      string
		origin    string
		fetchSite string
		requested string
	}{
		{name: "missing origin", fetchSite: "same-origin", requested: "intraktible"},
		{name: "null origin", origin: "null", fetchSite: "same-origin", requested: "intraktible"},
		{name: "mismatched origin", origin: "https://attacker.example", fetchSite: "same-origin", requested: "intraktible"},
		{name: "missing fetch metadata", origin: "https://intraktible.example.test", requested: "intraktible"},
		{name: "cross-site fetch metadata", origin: "https://intraktible.example.test", fetchSite: "cross-site", requested: "intraktible"},
		{name: "same-site subdomain fetch metadata", origin: "https://intraktible.example.test", fetchSite: "same-site", requested: "intraktible"},
		{name: "unknown fetch metadata", origin: "https://intraktible.example.test", fetchSite: "future-site", requested: "intraktible"},
		{name: "missing requested-with", origin: "https://intraktible.example.test", fetchSite: "same-origin"},
		{name: "wrong requested-with", origin: "https://intraktible.example.test", fetchSite: "same-origin", requested: "XMLHttpRequest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := auth.NewSessions()
			token, err := sessions.Issue(identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}, auth.RoleViewer, auth.ScopeAll)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "https://intraktible.example.test/v1/logout", http.NoBody)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			}
			if tc.requested != "" {
				req.Header.Set("X-Requested-With", tc.requested)
			}
			req.AddCookie(&http.Cookie{Name: "session", Value: token})
			rec := httptest.NewRecorder()
			httpx.LogoutHandler(sessions)(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("cross-origin logout -> %d, want 403", rec.Code)
			}
			if _, _, _, ok := sessions.Resolve(token); !ok {
				t.Fatal("rejected cross-origin logout revoked the session")
			}
		})
	}
}

func TestLogoutAcceptsSameOriginBrowserMetadata(t *testing.T) {
	sessions := auth.NewSessions()
	token, err := sessions.Issue(identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}, auth.RoleViewer, auth.ScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://intraktible.example.test/v1/logout", http.NoBody)
	setSameOriginLogoutHeaders(req, "https://intraktible.example.test")
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()
	httpx.LogoutHandler(sessions)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-origin logout -> %d body=%s", rec.Code, rec.Body.String())
	}
	if _, _, _, ok := sessions.Resolve(token); ok {
		t.Fatal("same-origin logout retained the session")
	}
}

func TestLogoutRevokesBeforeRejectingInvalidProviderMetadata(t *testing.T) {
	sessions := auth.NewSessions()
	token, err := sessions.IssueSSO(
		identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"},
		auth.RoleViewer,
		auth.ScopeAll,
		auth.SSOSession{
			Protocol: "oidc", Issuer: "https://auth.example.test", ClientID: "intraktible", IDToken: "signed.id.token",
			EndSessionEndpoint:    "https://auth.example.test/logout",
			PostLogoutRedirectURL: "https://attacker.example.test/not-the-app-landing",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/logout", http.NoBody)
	setSameOriginLogoutHeaders(req, "http://example.com")
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()
	httpx.LogoutHandler(sessions)(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("logout with invalid SSO metadata -> %d, want 500", rec.Code)
	}
	if _, _, _, ok := sessions.Resolve(token); ok {
		t.Fatal("provider logout failure retained the local session")
	}
	cleared := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "session" && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("provider logout failure did not clear the browser session cookie")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("logout Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
}

func TestLogoutRejectsStoredForeignEndSessionOriginWithoutDisclosingToken(t *testing.T) {
	sessions := auth.NewSessions()
	token, err := sessions.IssueSSO(
		identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"},
		auth.RoleViewer,
		auth.ScopeAll,
		auth.SSOSession{
			Protocol: "oidc", Issuer: "https://auth.example.test", ClientID: "intraktible", IDToken: "sensitive.id.token",
			EndSessionEndpoint:    "https://attacker.example/logout",
			PostLogoutRedirectURL: "https://intraktible.example.test/v1/auth/signed-out",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://intraktible.example.test/v1/logout", http.NoBody)
	setSameOriginLogoutHeaders(req, "https://intraktible.example.test")
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()
	httpx.LogoutHandler(sessions)(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("logout with foreign provider origin -> %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sensitive.id.token") || strings.Contains(rec.Header().Get("Location"), "sensitive.id.token") {
		t.Fatal("logout disclosed the stored ID token")
	}
	if _, _, _, ok := sessions.Resolve(token); ok {
		t.Fatal("provider metadata rejection retained the local session")
	}
}

func setSameOriginLogoutHeaders(req *http.Request, origin string) {
	req.Header.Set("Origin", origin)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("X-Requested-With", "intraktible")
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
