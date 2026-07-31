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
	"github.com/e6qu/intraktible/platform/store"
)

// shell stands in for the embedded SPA: it answers 200 with a page to anyone,
// which is exactly the behavior the gate exists to prevent from reaching an
// anonymous browser.
func shell() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html><title>app shell</title>"))
	})
}

func navigate(path string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, http.NoBody)
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.Header.Set("Accept", "text/html,application/xhtml+xml")
	return r
}

func newSessions(t *testing.T) *auth.StoreSessions {
	t.Helper()
	return auth.NewStoreSessions(store.NewMemory())
}

// The launch origin must not serve the application shell to a browser with no
// session: anything reading the server's response — an SSO validator sampling at
// domcontentloaded, a crawler — would see a 200 and conclude the app does not
// fail closed.
func TestBrowserGateRedirectsAnonymousLaunchOrigin(t *testing.T) {
	gate := httpx.BrowserGate(shell(), newSessions(t), httpx.SignInEntry{Path: "/v1/auth/signed-out"})

	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, navigate("/"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("anonymous GET / = %d, want 303 (must fail closed server-side)", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/v1/auth/signed-out" {
		t.Fatalf("Location = %q, want the sign-in entry point", got)
	}
	// net/http writes its own short hyperlink body for a redirected GET; what must
	// never appear is the application shell itself.
	if strings.Contains(rec.Body.String(), "app shell") {
		t.Fatalf("the redirect carried the shell: %s", rec.Body.String())
	}
}

// Deep links are protected too: /engine is as much a launch origin as / when a
// bookmark or an SSO catalog entry points at it.
func TestBrowserGateRedirectsAnonymousDeepLink(t *testing.T) {
	gate := httpx.BrowserGate(shell(), newSessions(t), httpx.SignInEntry{Path: "/login"})

	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, navigate("/engine/credit-risk"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("anonymous deep link = %d, want 303", rec.Code)
	}
}

// The gate must not close over its own entry point, or signing in becomes a
// redirect loop.
func TestBrowserGateAllowsSignInEntryAndPublicPaths(t *testing.T) {
	gate := httpx.BrowserGate(shell(), newSessions(t), httpx.SignInEntry{Path: "/login", Exempt: []string{"/login"}})

	for _, path := range []string{"/login", "/v1/auth/signed-out", "/healthz", "/readyz", "/version", "/openapi.json", "/docs"} {
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, navigate(path))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (must stay reachable without a session)", path, rec.Code)
		}
	}
}

// A real session is what the gate is checking for, so one must pass through
// untouched.
func TestBrowserGateServesShellWithSession(t *testing.T) {
	sessions := newSessions(t)
	tok, err := sessions.Issue(identity.Identity{Org: "demo", Workspace: "main", Actor: "operator"}, auth.RoleAdmin, auth.ScopeAll, false)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}

	gate := httpx.BrowserGate(shell(), sessions, httpx.SignInEntry{Path: "/login"})
	req := navigate("/")
	req.AddCookie(&http.Cookie{Name: "session", Value: tok})

	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated GET / = %d, want 200", rec.Code)
	}
}

// An expired or revoked cookie is not a session. Letting it through would hand
// back a shell that 401s on its first fetch — a worse experience than sign-in,
// and it would reopen the hole the gate closes.
func TestBrowserGateRejectsRevokedSession(t *testing.T) {
	sessions := newSessions(t)
	tok, err := sessions.Issue(identity.Identity{Org: "demo", Workspace: "main", Actor: "operator"}, auth.RoleAdmin, auth.ScopeAll, false)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	if err := sessions.Revoke(tok); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	gate := httpx.BrowserGate(shell(), sessions, httpx.SignInEntry{Path: "/login"})
	req := navigate("/")
	req.AddCookie(&http.Cookie{Name: "session", Value: tok})

	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("revoked-session GET / = %d, want 303", rec.Code)
	}
}

// Only top-level navigations get a redirect. An asset fetch or an XHR must fall
// through: the sign-in page's own stylesheet is requested without a session, and
// answering those with an HTML redirect would break the page that fixes the
// problem.
func TestBrowserGateIgnoresNonNavigationRequests(t *testing.T) {
	gate := httpx.BrowserGate(shell(), newSessions(t), httpx.SignInEntry{Path: "/login"})

	asset := httptest.NewRequest(http.MethodGet, "/_app/immutable/entry/start.js", http.NoBody)
	asset.Header.Set("Sec-Fetch-Mode", "no-cors")
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, asset)
	if rec.Code != http.StatusOK {
		t.Fatalf("asset fetch = %d, want 200 (not a navigation)", rec.Code)
	}

	xhr := httptest.NewRequest(http.MethodGet, "/some/data", http.NoBody)
	xhr.Header.Set("Accept", "application/json")
	rec = httptest.NewRecorder()
	gate.ServeHTTP(rec, xhr)
	if rec.Code != http.StatusOK {
		t.Fatalf("XHR = %d, want 200 (not a navigation)", rec.Code)
	}
}

// Belt and braces against a stale session store: Resolve is the authority, and a
// cookie whose token was never issued must not be mistaken for a session.
func TestBrowserGateRejectsForgedCookie(t *testing.T) {
	gate := httpx.BrowserGate(shell(), newSessions(t), httpx.SignInEntry{Path: "/login"})
	req := navigate("/")
	req.AddCookie(&http.Cookie{Name: "session", Value: "not-a-real-token"})

	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("forged-cookie GET / = %d, want 303", rec.Code)
	}
}
