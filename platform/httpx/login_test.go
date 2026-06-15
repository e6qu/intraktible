// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
)

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
	if gotID, ok := sessions.Resolve(token); !ok || gotID != id {
		t.Fatalf("issued session did not resolve to the key's identity: %v %v", gotID, ok)
	}

	// Logout revokes the session and clears the cookie.
	out := httptest.NewRecorder()
	logoutReq := httptest.NewRequest(http.MethodPost, "/v1/logout", http.NoBody)
	logoutReq.AddCookie(&http.Cookie{Name: "session", Value: token})
	httpx.LogoutHandler(sessions)(out, logoutReq)
	if out.Code != http.StatusNoContent {
		t.Fatalf("logout -> %d, want 204", out.Code)
	}
	if _, ok := sessions.Resolve(token); ok {
		t.Fatal("session should be revoked after logout")
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
