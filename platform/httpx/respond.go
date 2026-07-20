// SPDX-License-Identifier: AGPL-3.0-or-later

// Package httpx is the imperative-shell HTTP layer: JSON helpers, middleware,
// and server wiring. Domain packages stay pure and never import this.
package httpx

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

var (
	releaseRevision string
	releaseBuiltAt  string
)

// JSON writes v as an application/json response with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("httpx: encode response", "err", err)
	}
}

// Error writes a structured JSON error. A 4xx is the caller's own fault, so its
// message is returned verbatim (fail loudly — validation errors are meant to be
// read). A 5xx is a server fault whose message can leak internals (a DSN, a driver
// error, a wrapped chain of context): it is logged loudly server-side but the
// client receives only a generic message, so operational detail never crosses the
// trust boundary. The per-request access log (method/path/status) correlates it.
func Error(w http.ResponseWriter, status int, err error) {
	if status >= 500 {
		slog.Error("httpx: server error", "status", status, "err", err)
		JSON(w, status, map[string]string{"error": http.StatusText(status)})
		return
	}
	JSON(w, status, map[string]string{"error": err.Error()})
}

// DecodeJSON strictly decodes the request body into v (unknown fields rejected).
// MaxJSONBody caps a JSON request body (8 MiB) to guard against unbounded or abusive
// payloads. Endpoints that legitimately accept very large input stream it line-by-line
// instead of DecodeJSON (e.g. /decide/stream), so they are not bound by this.
const MaxJSONBody = 8 << 20

func DecodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, MaxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	// Reject trailing data after the first JSON value. Without this, a body like
	// `{"role":"viewer"}{"role":"admin"}` decodes using only the first object and
	// silently ignores the rest, weakening the strict-decode guarantee.
	if dec.More() {
		return errors.New("request body must contain a single JSON value")
	}
	return nil
}

// secureEqual reports whether a and b are equal, in constant time. Used to compare
// short-lived single-use CSRF tokens (OIDC state, SAML RelayState) so the byte
// comparison can't be turned into a timing oracle.
func secureEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Caller resolves the authenticated identity, writing a 401 and returning
// ok=false when none is present.
func Caller(w http.ResponseWriter, r *http.Request) (identity.Identity, bool) {
	id, ok := identity.From(r.Context())
	if !ok {
		Error(w, http.StatusUnauthorized, errors.New("authentication required"))
	}
	return id, ok
}

// Health returns a /healthz handler that reports liveness AND projection health:
// check is the projection runtime's error accessor (nil = healthy). A stalled
// projection (an apply error stopped the consumer) returns 503 "degraded" so an
// orchestrator can restart/depool rather than keep serving stale read models.
func Health(check func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if check != nil {
			if err := check(); err != nil {
				JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "degraded", "error": err.Error()})
				return
			}
		}
		JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// Ready is the readiness probe (distinct from liveness). It is 503 until the
// projection consumer is healthy AND has caught up to the event-log head — a fresh
// replica rebuilding its read models from seq 0 is "up" (Health is 200) but must
// NOT take traffic yet, or it would serve empty/stale reads during a rolling
// deploy. `applied` and `head` report progress in the body for ops. Once caught up
// a replica stays ready under steady-state write lag (it is depooled only if the
// projection actually stalls, which Health/check surfaces).
func Ready(applied, head func() uint64, check func() error) http.HandlerFunc {
	var caughtUp atomic.Bool
	return func(w http.ResponseWriter, _ *http.Request) {
		a, h := applied(), head()
		if check != nil {
			if err := check(); err != nil {
				JSON(w, http.StatusServiceUnavailable, map[string]any{"status": "degraded", "error": err.Error(), "applied": a, "head": h})
				return
			}
		}
		if !caughtUp.Load() {
			if a < h {
				JSON(w, http.StatusServiceUnavailable, map[string]any{"status": "rebuilding", "applied": a, "head": h})
				return
			}
			caughtUp.Store(true) // first catch-up latches; steady-state write lag no longer flaps readiness
		}
		JSON(w, http.StatusOK, map[string]any{"status": "ready", "applied": a, "head": h})
	}
}

// Version returns a /version handler reporting build metadata (VCS revision +
// Go toolchain) from the embedded build info — so ops can confirm exactly what is
// running. Read once at construction; unauthenticated, like /healthz.
func Version() http.HandlerFunc {
	rev, builtAt, gover := "unknown", releaseBuiltAt, runtime.Version()
	if bi, ok := debug.ReadBuildInfo(); ok {
		gover = bi.GoVersion
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				rev = s.Value
			}
		}
	}
	if releaseRevision != "" {
		rev = releaseRevision
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		JSON(w, http.StatusOK, map[string]string{
			"service":  "intraktible",
			"revision": rev,
			"built_at": builtAt,
			"go":       gover,
		})
	}
}

// Route is one declarative endpoint: an HTTP method, a 1.22-mux pattern, and its
// handler. A service registers a []Route via Register instead of repeating
// mux.HandleFunc lines.
type Route struct {
	Method  string
	Pattern string
	Handler http.HandlerFunc
}

// Register wires each route into the mux as "METHOD pattern".
func Register(mux *http.ServeMux, routes []Route) {
	for _, rt := range routes {
		mux.HandleFunc(rt.Method+" "+rt.Pattern, rt.Handler)
	}
}

// Emit is the shared write-endpoint shape: authenticate, decode the request body
// into req, run the command, and respond 202 with the resulting event id + seq.
// run is invoked after req is decoded, so its closure can read the decoded fields.
// A decode or command error maps to 400.
func Emit(w http.ResponseWriter, r *http.Request, req any, run func(identity.Identity) (eventlog.Envelope, error)) {
	id, ok := Caller(w, r)
	if !ok {
		return
	}
	if err := DecodeJSON(r, req); err != nil {
		Error(w, http.StatusBadRequest, err)
		return
	}
	e, err := run(id)
	if err != nil {
		Error(w, http.StatusBadRequest, err)
		return
	}
	JSON(w, http.StatusAccepted, map[string]any{"event_id": e.ID, "seq": e.Seq})
}

// Act is the shared path-parameterized write shape: authenticate, run the
// command, and respond 200 with the resulting event id + seq (400 on a command
// error). The body-less sibling of Emit, for deletes/revokes/acks addressed
// entirely by path parameters.
func Act(w http.ResponseWriter, r *http.Request, run func(identity.Identity) (eventlog.Envelope, error)) {
	id, ok := Caller(w, r)
	if !ok {
		return
	}
	e, err := run(id)
	if err != nil {
		Error(w, http.StatusBadRequest, err)
		return
	}
	JSON(w, http.StatusOK, map[string]any{"event_id": e.ID, "seq": e.Seq})
}

// Download writes body as a file attachment with the given content type and
// filename — the shared writer for the diagram-export and audit-export endpoints.
func Download(w http.ResponseWriter, contentType, filename, body string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(filename)+"\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// sanitizeFilename neutralizes characters that could break out of the quoted
// Content-Disposition filename — CR/LF (header injection / response splitting),
// double-quote and backslash (quote escape), and other control bytes. Defense in
// depth so the helper is safe-by-construction even if a caller ever passes
// user-controlled text (today's callers use fixed/validated names).
func sanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r < 0x20 {
			return '_'
		}
		return r
	}, name)
}

// WriteList responds with a read-model listing under the given JSON key, mapping
// a store error to 500.
func WriteList[T any](w http.ResponseWriter, key string, items []T, err error) {
	if err != nil {
		Error(w, http.StatusInternalServerError, err)
		return
	}
	JSON(w, http.StatusOK, map[string]any{key: items})
}

// WriteOne responds with a single read-model document, mapping a store error to
// 500 and a missing document to 404 with notFound.
func WriteOne[T any](w http.ResponseWriter, item T, found bool, err error, notFound string) {
	if err != nil {
		Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		Error(w, http.StatusNotFound, errors.New(notFound))
		return
	}
	JSON(w, http.StatusOK, item)
}
