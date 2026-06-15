// SPDX-License-Identifier: AGPL-3.0-or-later

// Package httpx is the imperative-shell HTTP layer: JSON helpers, middleware,
// and server wiring. Domain packages stay pure and never import this.
package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

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
