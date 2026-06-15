// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
)

func TestRequestID(t *testing.T) {
	var seen string
	h := httpx.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Request-Id")
	}))
	// Generates one when absent.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if w.Header().Get("X-Request-Id") == "" {
		t.Fatal("expected a generated request id in the response header")
	}
	// Echoes a supplied one.
	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	r.Header.Set("X-Request-Id", "abc123")
	h.ServeHTTP(w, r)
	if w.Header().Get("X-Request-Id") != "abc123" || seen != "abc123" {
		t.Fatalf("request id not echoed: header=%q seen=%q", w.Header().Get("X-Request-Id"), seen)
	}
}

func TestRecover(t *testing.T) {
	h := httpx.Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("panic should become 500, got %d", w.Code)
	}
}

func TestChainOrder(t *testing.T) {
	var order []string
	mw := func(tag string) httpx.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, tag)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := httpx.Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		order = append(order, "handler")
	}), mw("first"), mw("second"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if got := len(order); got != 3 || order[0] != "first" || order[1] != "second" || order[2] != "handler" {
		t.Fatalf("chain order = %v (first listed must be outermost)", order)
	}
}

func TestAuthenticateAPIKey(t *testing.T) {
	kr := auth.NewKeyring()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	kr.Add("good-key", auth.APIKey{ID: "k", Identity: id, Scope: auth.Sandbox})
	sessions := auth.NewSessions()

	// Downstream handler asserts the identity + scope were injected.
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := identity.From(r.Context())
		if !ok || got != id {
			t.Errorf("identity not propagated: %+v ok=%v", got, ok)
		}
		if sc, ok := httpx.Scope(r.Context()); !ok || sc != auth.Sandbox {
			t.Errorf("scope not propagated: %v ok=%v", sc, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h := httpx.Authenticate(kr, sessions)(downstream)

	// serveWithKey runs a GET through h with the given X-Api-Key (empty = omit).
	serveWithKey := func(apiKey string) int {
		r := httptest.NewRequest(http.MethodGet, "/v1/x", http.NoBody)
		if apiKey != "" {
			r.Header.Set("X-Api-Key", apiKey)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	if got := serveWithKey("good-key"); got != http.StatusNoContent {
		t.Fatalf("valid key -> %d, want 204", got)
	}
	if got := serveWithKey("bad-key"); got != http.StatusUnauthorized {
		t.Fatalf("invalid key -> %d, want 401", got)
	}
	if got := serveWithKey(""); got != http.StatusUnauthorized {
		t.Fatalf("no creds -> %d, want 401", got)
	}
}

func TestAuthorizeRBAC(t *testing.T) {
	kr := auth.NewKeyring()
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "u"}
	for secret, role := range map[string]auth.Role{
		"viewer-k": auth.RoleViewer, "operator-k": auth.RoleOperator,
		"editor-k": auth.RoleEditor, "approver-k": auth.RoleApprover, "admin-k": auth.RoleAdmin,
	} {
		kr.Add(secret, auth.APIKey{ID: secret, Identity: id, Role: role})
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := httpx.Chain(ok, httpx.Authenticate(kr, auth.NewSessions()), httpx.Authorize)

	do := func(secret, method, path string) int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, http.NoBody)
		r.Header.Set("X-Api-Key", secret)
		h.ServeHTTP(w, r)
		return w.Code
	}

	cases := []struct {
		secret, method, path string
		want                 int
	}{
		{"viewer-k", "GET", "/v1/flows", 200},                                    // reads open to viewer
		{"viewer-k", "POST", "/v1/flows", 403},                                   // authoring needs editor role
		{"editor-k", "POST", "/v1/flows", 200},                                   // editor may author
		{"editor-k", "POST", "/v1/flows/f1/versions", 200},                       // publish needs editor
		{"editor-k", "POST", "/v1/flows/f1/deployments", 403},                    // deploy needs approver role
		{"approver-k", "POST", "/v1/flows/f1/deployments", 200},                  // approver may deploy
		{"viewer-k", "POST", "/v1/flows/s/production/decide", 403},               // decide needs operator role
		{"operator-k", "POST", "/v1/flows/s/production/decide", 200},             // operator may decide
		{"operator-k", "POST", "/v1/cases", 200},                                 // case ops need operator
		{"operator-k", "POST", "/v1/agents", 403},                                // defining an agent needs editor
		{"editor-k", "POST", "/v1/agents", 200},                                  // editor may define
		{"admin-k", "POST", "/v1/flows/f1/deployments", 200},                     // admin may do anything
		{"editor-k", "POST", "/v1/flows/f1/deployment-requests", 200},            // propose: editor
		{"operator-k", "POST", "/v1/flows/f1/deployment-requests", 403},          // propose needs editor
		{"editor-k", "POST", "/v1/flows/f1/deployment-requests/r1/approve", 403}, // approve needs approver
		{"approver-k", "POST", "/v1/flows/f1/deployment-requests/r1/approve", 200},
	}
	for _, c := range cases {
		if got := do(c.secret, c.method, c.path); got != c.want {
			t.Errorf("%s %s as %s -> %d, want %d", c.method, c.path, c.secret, got, c.want)
		}
	}
}

func TestAuthenticateSession(t *testing.T) {
	kr := auth.NewKeyring()
	sessions := auth.NewSessions()
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "u"}
	tok := sessions.Issue(id, auth.RoleEditor)

	h := httpx.Authenticate(kr, sessions)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, ok := identity.From(r.Context()); !ok || got != id {
			t.Errorf("session identity not propagated: %+v", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/x", http.NoBody)
	r.AddCookie(&http.Cookie{Name: "session", Value: tok})
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("valid session -> %d", w.Code)
	}
}
