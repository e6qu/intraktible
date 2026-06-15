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

func TestAuthenticateSession(t *testing.T) {
	kr := auth.NewKeyring()
	sessions := auth.NewSessions()
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "u"}
	tok := sessions.Issue(id)

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
