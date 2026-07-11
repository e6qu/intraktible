// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e6qu/intraktible/platform/httpx"
)

func TestReadyGatesUntilCaughtUp(t *testing.T) {
	var applied uint64
	const head = 100
	h := httpx.Ready(func() uint64 { return applied }, func() uint64 { return head }, func() error { return nil })

	// Rebuilding: applied < head → 503.
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("rebuilding readiness = %d, want 503", rec.Code)
	}

	// Caught up → 200, and it latches: a subsequent momentary lag stays ready.
	applied = head
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("caught-up readiness = %d, want 200", rec.Code)
	}
	applied = head - 5 // steady-state write lag after first catch-up
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("post-catchup lag readiness = %d, want 200 (must not flap)", rec.Code)
	}
}

func TestReadyReportsProjectionStall(t *testing.T) {
	h := httpx.Ready(func() uint64 { return 10 }, func() uint64 { return 10 }, func() error { return errStall })
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("stalled readiness = %d, want 503", rec.Code)
	}
}

var errStall = &stallErr{}

type stallErr struct{}

func (*stallErr) Error() string { return "projector stalled" }

func TestRateLimitAllowsBurstThenBlocks(t *testing.T) {
	limit := httpx.NewRateLimit(1, 3) // 1 rps, burst 3
	var served int
	h := limit(func(w http.ResponseWriter, _ *http.Request) { served++; w.WriteHeader(http.StatusOK) })

	statuses := make([]int, 0, 5)
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/login", http.NoBody)
		req.RemoteAddr = "203.0.113.7:5555"
		h(rec, req)
		statuses = append(statuses, rec.Code)
	}
	// First 3 (burst) pass, the rest are limited.
	for i := 0; i < 3; i++ {
		if statuses[i] != http.StatusOK {
			t.Fatalf("burst request %d = %d, want 200", i, statuses[i])
		}
	}
	if statuses[3] != http.StatusTooManyRequests || statuses[4] != http.StatusTooManyRequests {
		t.Fatalf("over-burst requests = %v, want 429s", statuses[3:])
	}
	if served != 3 {
		t.Fatalf("handler served %d requests, want 3 (the limiter blocked the rest)", served)
	}
}

func TestRateLimitIsPerClientIP(t *testing.T) {
	limit := httpx.NewRateLimit(1, 1) // burst 1
	h := limit(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// Two different IPs each get their own bucket.
	for _, ip := range []string{"198.51.100.1:1", "198.51.100.2:1"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/login", http.NoBody)
		req.RemoteAddr = ip
		h(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("first request from %s = %d, want 200", ip, rec.Code)
		}
	}
}
