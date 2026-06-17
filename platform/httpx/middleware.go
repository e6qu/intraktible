// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/identity"
)

// Middleware decorates an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware in order (first listed is outermost).
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

type ctxKey int

const (
	reqIDKey ctxKey = iota
	scopeKey
	roleKey
)

// RequestID assigns a request id and echoes it in the X-Request-Id header.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			var b [8]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), reqIDKey, id)))
	})
}

// Recover turns panics into 500s instead of crashing the server.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				slog.Error("httpx: panic", "value", v, "path", r.URL.Path)
				Error(w, http.StatusInternalServerError, errors.New("internal error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// Logger logs each request with method, path, status, and duration.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", sw.status, "dur", time.Since(start))
	})
}

// Authenticate resolves an identity from an X-Api-Key header or a session
// cookie and rejects unauthenticated requests with 401 (fail loudly).
func Authenticate(keyring *auth.Keyring, sessions auth.SessionStore) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if secret := r.Header.Get("X-Api-Key"); secret != "" {
				if key, ok := keyring.Resolve(secret); ok {
					ctx := identity.With(r.Context(), key.Identity)
					ctx = context.WithValue(ctx, scopeKey, key.Scope)
					ctx = context.WithValue(ctx, roleKey, auth.ParseRole(string(key.Role)))
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				Error(w, http.StatusUnauthorized, errors.New("invalid api key"))
				return
			}
			if c, err := r.Cookie("session"); err == nil {
				if id, role, ok := sessions.Resolve(c.Value); ok {
					ctx := context.WithValue(identity.With(r.Context(), id), roleKey, auth.ParseRole(string(role)))
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			Error(w, http.StatusUnauthorized, errors.New("authentication required"))
		})
	}
}

// Scope returns the API-key scope for the request, if any.
func Scope(ctx context.Context) (auth.Scope, bool) {
	s, ok := ctx.Value(scopeKey).(auth.Scope)
	return s, ok
}

// RoleOf returns the authenticated principal's role (viewer when unset).
func RoleOf(ctx context.Context) auth.Role {
	if r, ok := ctx.Value(roleKey).(auth.Role); ok {
		return r
	}
	return auth.RoleViewer
}

// Authorize enforces the minimum role for the request (derived from method+path),
// returning 403 when the principal's role is insufficient. It runs after
// Authenticate, so a role is always present in context.
func Authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		need := requiredRole(r.Method, r.URL.Path)
		if !RoleOf(r.Context()).AtLeast(need) {
			Error(w, http.StatusForbidden, fmt.Errorf("requires at least the %q role", need))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requiredRole maps a request to the minimum role it needs. Reads are open to any
// authenticated viewer; deploying/approving a version is the highest bar; authoring
// (defining flows/agents/connectors/features) needs editor; all other mutations are
// runtime operations (decide, cases, agent runs, context ingest) at operator level.
func requiredRole(method, path string) auth.Role {
	// The audit surface exposes every actor's activity across the tenant. It is
	// read-only but sensitive, so it is gated to admins regardless of method —
	// checked before the general read rule below.
	if path == "/v1/audit" {
		return auth.RoleAdmin
	}
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
		return auth.RoleViewer
	}
	switch {
	case strings.Contains(path, "/deployments"), // a direct deploy (non-prod)
		strings.HasSuffix(path, "/approve"), // the checker approving a deployment
		strings.HasSuffix(path, "/reject"):  // the checker rejecting a deployment
		return auth.RoleApprover
	case strings.HasSuffix(path, "/deployment-requests"), // proposing a deployment (maker)
		isAuthoringPath(path):
		return auth.RoleEditor
	default:
		return auth.RoleOperator
	}
}

// isAuthoringPath reports whether a mutating path defines/edits decision logic
// (vs. running it). These are the create/publish endpoints.
func isAuthoringPath(path string) bool {
	return path == "/v1/flows" || // create a flow
		path == "/v1/policies" || // create a policy
		path == "/v1/preapprovals" || // grant a pre-approval (material)
		strings.HasSuffix(path, "/preapprove/batch") || // bulk-grant pre-approvals from a run
		strings.Contains(path, "/monitors") || // define/delete a monitor; check pushes alerts
		strings.HasPrefix(path, "/v1/webhooks") || // register/remove a notification endpoint
		strings.HasSuffix(path, "/versions") || // publish a flow or policy version
		path == "/v1/agents" || // define an agent
		path == "/v1/context/features" || // define a feature
		path == "/v1/context/connectors" // define a connector
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush and Hijack make the logging wrapper transparent to the optional
// interfaces that streaming needs: http.Flusher for Server-Sent Events and
// http.Hijacker for WebSocket upgrades. Without these the wrapper would mask the
// underlying writer's support and break streaming endpoints.
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := s.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("httpx: ResponseWriter does not support hijacking")
}
