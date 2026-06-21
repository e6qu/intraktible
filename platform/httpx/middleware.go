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
	"github.com/e6qu/intraktible/platform/metrics"
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
	principalKey
)

// Principal is the authenticated caller's authorization context: the role and the
// environment scope resolved by Authenticate. Both auth paths (API-key header and
// session cookie) set it as a SINGLE context value, so a caller can never carry a
// role without its scope — the gap that previously let a session minted from a
// sandbox-scoped key silently widen to every environment.
type Principal struct {
	Role  auth.Role
	Scope auth.Scope
}

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

// Recover turns panics into 500s instead of crashing the server. It wraps the
// writer to know whether the response was already committed: a panic mid-stream
// (an SSE/WebSocket handler that already wrote headers + chunks) must NOT then emit
// a second WriteHeader + JSON body — that superfluous write corrupts the response
// and logs a spurious error. statusWriter forwards Flush/Hijack, so streaming is
// unaffected.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			if v := recover(); v != nil {
				slog.Error("httpx: panic", "value", v, "path", r.URL.Path)
				if sw.wrote {
					return // response already started — can't cleanly send a 500
				}
				Error(sw, http.StatusInternalServerError, errors.New("internal error"))
			}
		}()
		next.ServeHTTP(sw, r)
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

// Metrics records each request's count + latency in Prometheus, keyed by the
// matched ServeMux route pattern (set on the request during dispatch — low
// cardinality, so per-ID paths don't explode the series). Place it in the outer
// chain so it observes the final status and the pattern resolved by nested muxes.
func Metrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		metrics.RecordHTTP(r.Pattern, r.Method, sw.status, time.Since(start))
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
					ctx = withPrincipal(ctx, Principal{Role: auth.ParseRole(string(key.Role)), Scope: key.Scope})
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				Error(w, http.StatusUnauthorized, errors.New("invalid api key"))
				return
			}
			if c, err := r.Cookie("session"); err == nil {
				if id, role, scope, ok := sessions.Resolve(c.Value); ok {
					ctx := identity.With(r.Context(), id)
					ctx = withPrincipal(ctx, Principal{Role: auth.ParseRole(string(role)), Scope: scope})
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			Error(w, http.StatusUnauthorized, errors.New("authentication required"))
		})
	}
}

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// PrincipalOf returns the authenticated caller's authorization context. ok is
// false only for an unauthenticated request (no middleware ran).
func PrincipalOf(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}

// Scope returns the caller's environment scope. ok is false for an unauthenticated
// request; an authenticated caller (key or session) always carries a scope.
func Scope(ctx context.Context) (auth.Scope, bool) {
	if p, ok := PrincipalOf(ctx); ok {
		return p.Scope, true
	}
	return "", false
}

// RoleOf returns the authenticated principal's role (viewer when unset).
func RoleOf(ctx context.Context) auth.Role {
	if p, ok := PrincipalOf(ctx); ok {
		return p.Role
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

// AuthorizeRoutes is Authorize that classifies a request by its MATCHED ROUTE
// TEMPLATE (via mux.Handler) rather than the raw URL path. requiredRole then matches
// against the fixed route pattern (e.g. "/v1/flows/{id}/monitors"), so no user-
// controlled path segment (a flow id/slug) can influence the role decision. This is
// the form wired in production, where the v1 mux is available to introspect.
func AuthorizeRoutes(mux *http.ServeMux) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route := r.URL.Path
			if mux != nil {
				if _, pattern := mux.Handler(r); pattern != "" {
					route = patternPath(pattern)
				}
			}
			need := requiredRole(r.Method, route)
			if !RoleOf(r.Context()).AtLeast(need) {
				Error(w, http.StatusForbidden, fmt.Errorf("requires at least the %q role", need))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// patternPath strips the optional "METHOD " (and host) prefix from a ServeMux
// pattern, leaving the path template — e.g. "POST /v1/x/{id}" -> "/v1/x/{id}".
func patternPath(pattern string) string {
	if i := strings.IndexByte(pattern, '/'); i >= 0 {
		return pattern[i:]
	}
	return pattern
}

// requiredRole maps a request to the minimum role it needs. Reads are open to any
// authenticated viewer; deploying/approving a version is the highest bar; authoring
// (defining flows/agents/connectors/features) needs editor; all other mutations are
// runtime operations (decide, cases, agent runs, context ingest) at operator level.
func requiredRole(method, path string) auth.Role {
	// The audit surface exposes every actor's activity across the tenant. It is
	// read-only but sensitive, so it is gated to admins regardless of method —
	// checked before the general read rule below.
	if path == "/v1/audit" || strings.HasPrefix(path, "/v1/audit/") ||
		strings.HasPrefix(path, "/v1/api-keys") || strings.HasPrefix(path, "/v1/erasure") {
		return auth.RoleAdmin
	}
	// The streaming run endpoints are GET (EventSource/WebSocket are GET-only) but
	// they MUTATE — each invokes the agent (a billable provider call) and records a
	// run. Gate them like the POST run path, before the "all GETs are reads" rule.
	if strings.HasSuffix(path, "/run/stream") || strings.HasSuffix(path, "/run/ws") {
		return auth.RoleOperator
	}
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
		return auth.RoleViewer
	}
	// Changing the PII masking config is a compliance control — admin only.
	if path == "/v1/privacy" {
		return auth.RoleAdmin
	}
	// A user manages their own notification inbox (marking read) — any viewer.
	if strings.HasPrefix(path, "/v1/notifications") {
		return auth.RoleViewer
	}
	switch {
	case strings.Contains(path, "/deployments"), // a direct deploy (non-prod)
		strings.HasSuffix(path, "/promote"),          // promote a live version up the chain
		strings.HasSuffix(path, "/promotion-policy"), // configure promotion gates
		strings.HasSuffix(path, "/approve"),          // the checker approving a deployment
		strings.HasSuffix(path, "/reject"):           // the checker rejecting a deployment
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
		path == "/v1/flows/import" || // import a flow-as-code document (create + publish)
		path == "/v1/flows/import-bundle" || // import many flows at once (GitOps repo)
		path == "/v1/policies" || // create a policy
		path == "/v1/preapprovals" || // grant a pre-approval (material)
		strings.HasSuffix(path, "/preapprove/batch") || // bulk-grant pre-approvals from a run
		strings.Contains(path, "/monitors") || // define/delete a monitor; check pushes alerts
		strings.HasSuffix(path, "/assertions") || // define a flow's test cases (run is separate)
		strings.HasSuffix(path, "/shadow") || // assign a shadow version (PUT; GET is a viewer read)
		strings.HasPrefix(path, "/v1/webhooks") || // register/remove a notification endpoint
		strings.HasSuffix(path, "/versions") || // publish a flow or policy version
		path == "/v1/agents" || // define an agent
		path == "/v1/context/features" || // define a feature
		path == "/v1/context/connectors" // define a connector
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool // whether headers or body have been committed (for Recover's guard)
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.wrote = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK // an implicit 200 from a bodyless WriteHeader
	}
	s.wrote = true
	return s.ResponseWriter.Write(b)
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
