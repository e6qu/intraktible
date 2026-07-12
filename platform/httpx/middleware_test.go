// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx_test

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestSecurityHeaders(t *testing.T) {
	h := httpx.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	want := map[string]string{
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Content-Security-Policy": "default-src 'self'; ",
	}
	for k, v := range want {
		got := w.Header().Get(k)
		if k == "Content-Security-Policy" {
			if got == "" || !strings.HasPrefix(got, v) || !strings.Contains(got, "frame-ancestors 'none'") {
				t.Fatalf("CSP = %q, want prefix %q including frame-ancestors 'none'", got, v)
			}
			continue
		}
		if got != v {
			t.Fatalf("%s = %q, want %q", k, got, v)
		}
	}
	// HSTS is asserted only over TLS — a plaintext request must NOT carry it.
	if got := w.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("HSTS set on a plaintext request: %q", got)
	}

	// Over TLS, HSTS is present.
	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "https://example.test/", http.NoBody)
	r.TLS = &tls.ConnectionState{}
	h.ServeHTTP(w, r)
	if got := w.Header().Get("Strict-Transport-Security"); got == "" {
		t.Fatal("HSTS missing on a TLS request")
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
		{"viewer-k", "GET", "/v1/flows", 200},                        // reads open to viewer
		{"viewer-k", "POST", "/v1/flows", 403},                       // authoring needs editor role
		{"editor-k", "POST", "/v1/flows", 200},                       // editor may author
		{"editor-k", "POST", "/v1/flows/f1/versions", 200},           // publish needs editor
		{"operator-k", "PATCH", "/v1/flows/f1", 403},                 // editing flow details needs editor
		{"editor-k", "PATCH", "/v1/flows/f1", 200},                   // editor may edit details
		{"editor-k", "POST", "/v1/flows/f1/deployments", 403},        // deploy needs approver role
		{"approver-k", "POST", "/v1/flows/f1/deployments", 200},      // approver may deploy
		{"viewer-k", "POST", "/v1/flows/s/production/decide", 403},   // decide needs operator role
		{"operator-k", "POST", "/v1/flows/s/production/decide", 200}, // operator may decide
		{"operator-k", "POST", "/v1/cases", 200},                     // case ops need operator
		{"operator-k", "POST", "/v1/agents", 403},                    // defining an agent needs editor
		{"editor-k", "POST", "/v1/agents", 200},                      // editor may define
		// A model's coefficients are decision logic: defining/training one is editor,
		// not operator (an operator must not be able to swap the model a live flow uses).
		{"operator-k", "POST", "/v1/models", 403},
		{"editor-k", "POST", "/v1/models", 200},
		{"operator-k", "POST", "/v1/models/train", 403},
		{"editor-k", "POST", "/v1/models/train", 200},
		{"operator-k", "POST", "/v1/models/m1/monitor", 403},
		{"editor-k", "POST", "/v1/models/m1/monitor", 200},
		// Recording a realized outcome is runtime feedback, not authoring → operator.
		{"operator-k", "POST", "/v1/models/m1/outcomes", 200},
		{"viewer-k", "GET", "/v1/models/m1/performance", 200},                    // reads stay open to viewer
		{"admin-k", "POST", "/v1/flows/f1/deployments", 200},                     // admin may do anything
		{"editor-k", "POST", "/v1/flows/f1/deployment-requests", 200},            // propose: editor
		{"operator-k", "POST", "/v1/flows/f1/deployment-requests", 403},          // propose needs editor
		{"editor-k", "POST", "/v1/flows/f1/deployment-requests/r1/approve", 403}, // approve needs approver
		{"approver-k", "POST", "/v1/flows/f1/deployment-requests/r1/approve", 200},
		{"admin-k", "GET", "/v1/audit", 200},    // the audit trail is admin-only
		{"approver-k", "GET", "/v1/audit", 403}, // even approver cannot read it
		{"viewer-k", "GET", "/v1/audit", 403},   // ...nor viewer
		{"viewer-k", "GET", "/v1/privacy", 200}, // the masking config is readable
		{"editor-k", "PUT", "/v1/privacy", 403}, // ...but changing it is admin-only
		{"admin-k", "PUT", "/v1/privacy", 200},  // a compliance control
		// The streaming run endpoints are GET but MUTATE (invoke + record a run), so
		// they need operator like POST /run — a viewer must not trigger billable runs.
		{"viewer-k", "GET", "/v1/agents/a1/run/stream", 403},
		{"viewer-k", "GET", "/v1/agents/a1/run/ws", 403},
		{"operator-k", "GET", "/v1/agents/a1/run/stream", 200},
		{"operator-k", "GET", "/v1/agents/a1/run/ws", 200},
	}
	for _, c := range cases {
		if got := do(c.secret, c.method, c.path); got != c.want {
			t.Errorf("%s %s as %s -> %d, want %d", c.method, c.path, c.secret, got, c.want)
		}
	}
}

// TestAuthorizeRoutesByPattern checks AuthorizeRoutes classifies by the MATCHED
// route template, not the raw path: a flow id segment that resembles a sensitive
// route ("audit", "monitors") is still classified by the {id} template it matched,
// so a user-controlled segment can never elevate or downgrade the required role.
func TestAuthorizeRoutesByPattern(t *testing.T) {
	kr := auth.NewKeyring()
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "u"}
	kr.Add("viewer-k", auth.APIKey{ID: "v", Identity: id, Role: auth.RoleViewer})
	kr.Add("editor-k", auth.APIKey{ID: "e", Identity: id, Role: auth.RoleEditor})
	kr.Add("admin-k", auth.APIKey{ID: "a", Identity: id, Role: auth.RoleAdmin})

	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/flows/{id}", ok)
	mux.HandleFunc("POST /v1/flows/{id}/monitors", ok)
	mux.HandleFunc("GET /v1/audit", ok)
	mux.HandleFunc("GET /v1/adverse-actions", ok)
	mux.HandleFunc("POST /v1/decisions/{decision_id}/adverse-action/issue", ok)
	mux.HandleFunc("GET /v1/consent/records", ok)
	mux.HandleFunc("GET /v1/compliance/jurisdiction", ok)
	mux.HandleFunc("PUT /v1/compliance/jurisdiction", ok)
	h := httpx.Chain(mux, httpx.Authenticate(kr, auth.NewSessions()), httpx.AuthorizeRoutes(mux))

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
		{"editor-k", "POST", "/v1/flows/f1/monitors", 200},                 // monitors template → editor
		{"viewer-k", "POST", "/v1/flows/f1/monitors", 403},                 // ...not viewer
		{"viewer-k", "GET", "/v1/flows/monitors", 200},                     // id="monitors" matches {id} → viewer read, NOT editor
		{"viewer-k", "GET", "/v1/flows/audit", 200},                        // id="audit" matches {id} → viewer, NOT admin
		{"admin-k", "GET", "/v1/audit", 200},                               // the real audit route → admin
		{"viewer-k", "GET", "/v1/audit", 403},                              // ...denied to viewer
		{"viewer-k", "GET", "/v1/adverse-actions", 200},                    // the read-only queue → viewer
		{"viewer-k", "POST", "/v1/decisions/d1/adverse-action/issue", 403}, // ...but issuing → operator+
		{"editor-k", "POST", "/v1/decisions/d1/adverse-action/issue", 200}, // editor outranks operator
		{"viewer-k", "GET", "/v1/consent/records", 200},                    // cross-subject consent read → viewer
		{"viewer-k", "GET", "/v1/compliance/jurisdiction", 200},            // reading the regimes → viewer
		{"viewer-k", "PUT", "/v1/compliance/jurisdiction", 403},            // setting them → admin
		{"admin-k", "PUT", "/v1/compliance/jurisdiction", 200},             // ...admin may
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
	tok, _ := sessions.Issue(id, auth.RoleEditor, auth.ScopeAll)

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

// TestCSRFHeaderGate covers the cookie-auth CSRF mitigation: a state-changing
// (non-GET) request authenticated by the session cookie must carry X-Requested-With
// or be rejected 403; an API-key-authenticated mutation is exempt (browsers don't
// auto-send X-Api-Key, so it can't be forged).
func TestCSRFHeaderGate(t *testing.T) {
	kr := auth.NewKeyring()
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "u"}
	kr.Add("good-key", auth.APIKey{ID: "k", Identity: id, Scope: auth.ScopeAll})
	sessions := auth.NewSessions()
	tok, _ := sessions.Issue(id, auth.RoleEditor, auth.ScopeAll)

	h := httpx.Authenticate(kr, sessions)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	serve := func(setup func(*http.Request)) int {
		r := httptest.NewRequest(http.MethodPost, "/v1/x", http.NoBody)
		setup(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	// Cookie-auth POST without the header -> 403.
	if got := serve(func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "session", Value: tok})
	}); got != http.StatusForbidden {
		t.Fatalf("cookie POST without X-Requested-With -> %d, want 403", got)
	}
	// Cookie-auth POST with the header -> ok.
	if got := serve(func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "session", Value: tok})
		r.Header.Set("X-Requested-With", "intraktible")
	}); got != http.StatusNoContent {
		t.Fatalf("cookie POST with X-Requested-With -> %d, want 204", got)
	}
	// API-key POST without the header -> ok (exempt).
	if got := serve(func(r *http.Request) {
		r.Header.Set("X-Api-Key", "good-key")
	}); got != http.StatusNoContent {
		t.Fatalf("api-key POST without X-Requested-With -> %d, want 204 (must stay exempt)", got)
	}
	// Cookie-auth GET (a read) without the header -> ok (safe method).
	rg := httptest.NewRequest(http.MethodGet, "/v1/x", http.NoBody)
	rg.AddCookie(&http.Cookie{Name: "session", Value: tok})
	wg := httptest.NewRecorder()
	h.ServeHTTP(wg, rg)
	if wg.Code != http.StatusNoContent {
		t.Fatalf("cookie GET without X-Requested-With -> %d, want 204", wg.Code)
	}
}

// Regression: a session minted from a SANDBOX-scoped key must expose only the
// sandbox scope to the request, so the environment gate cannot be escaped by
// exchanging a scoped key for a session. Previously the session carried no scope
// and the gate treated that as "any environment".
func TestSessionCarriesScope(t *testing.T) {
	kr := auth.NewKeyring()
	sessions := auth.NewSessions()
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "u"}
	tok, _ := sessions.Issue(id, auth.RoleOperator, auth.Sandbox)

	var gotScope auth.Scope
	var gotOK bool
	h := httpx.Authenticate(kr, sessions)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotScope, gotOK = httpx.Scope(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	r := httptest.NewRequest(http.MethodGet, "/v1/x", http.NoBody)
	r.AddCookie(&http.Cookie{Name: "session", Value: tok})
	h.ServeHTTP(httptest.NewRecorder(), r)

	if !gotOK || gotScope != auth.Sandbox {
		t.Fatalf("session scope = %q ok=%v, want sandbox/true", gotScope, gotOK)
	}
	if gotScope.Allows("production") {
		t.Fatal("a sandbox-scoped session must not permit the production environment")
	}
}
