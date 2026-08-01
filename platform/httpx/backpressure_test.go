// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBackpressureShedsReadLoadOverThreshold(t *testing.T) {
	t.Parallel()
	var lag uint64 = 50
	h := Backpressure(
		func() uint64 { return 100 - lag },
		func() uint64 { return 100 },
		10,
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Read over threshold is shed with 503 + Retry-After.
	r := httptest.NewRequest(http.MethodGet, "/v1/flows", http.NoBody)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("read over threshold -> %d, want 503", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("expected a Retry-After header")
	}

	// Writes are always admitted (they advance the log).
	r = httptest.NewRequest(http.MethodPost, "/v1/flows", http.NoBody)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("write over threshold -> %d, want 200", w.Code)
	}

	// Read under threshold is admitted.
	lag = 5
	r = httptest.NewRequest(http.MethodGet, "/v1/flows", http.NoBody)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("read under threshold -> %d, want 200", w.Code)
	}
}

func TestBackpressureZeroThresholdIsPassthrough(t *testing.T) {
	t.Parallel()
	h := Backpressure(func() uint64 { return 0 }, func() uint64 { return 1000 }, 0)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)
	r := httptest.NewRequest(http.MethodGet, "/v1/flows", http.NoBody)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("disabled gate -> %d, want 200", w.Code)
	}
}
