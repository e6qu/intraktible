// SPDX-License-Identifier: AGPL-3.0-or-later

// Package httpx is the imperative-shell HTTP layer: JSON helpers, middleware,
// and server wiring. Domain packages stay pure and never import this.
package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
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

// Error writes a structured JSON error. We surface the real message (fail
// loudly) rather than masking it behind a generic string.
func Error(w http.ResponseWriter, status int, err error) {
	JSON(w, status, map[string]string{"error": err.Error()})
}

// DecodeJSON strictly decodes the request body into v (unknown fields rejected).
func DecodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
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
