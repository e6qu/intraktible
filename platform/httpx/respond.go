// SPDX-License-Identifier: AGPL-3.0-or-later

// Package httpx is the imperative-shell HTTP layer: JSON helpers, middleware,
// and server wiring. Domain packages stay pure and never import this.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
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
