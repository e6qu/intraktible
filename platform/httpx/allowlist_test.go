// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIPAllowlistAdmitsAllowlistedAndRejectsOthers(t *testing.T) {
	t.Parallel()
	al, err := ParseIPAllowlist("10.0.0.0/8, 192.168.1.0/24")
	if err != nil {
		t.Fatal(err)
	}
	h := al.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Allowlisted IP passes.
	r1 := httptest.NewRequest(http.MethodGet, "/v1/flows", http.NoBody)
	r1.RemoteAddr = "10.1.2.3:12345"
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, r1)
	if w1.Code != http.StatusOK {
		t.Fatalf("allowlisted IP -> %d, want 200", w1.Code)
	}

	// Non-allowlisted IP is rejected.
	r2 := httptest.NewRequest(http.MethodGet, "/v1/flows", http.NoBody)
	r2.RemoteAddr = "8.8.8.8:12345"
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("non-allowlisted IP -> %d, want 403", w2.Code)
	}
}

func TestEmptyIPAllowlistAdmitsEveryone(t *testing.T) {
	t.Parallel()
	al, err := ParseIPAllowlist("")
	if err != nil {
		t.Fatal(err)
	}
	h := al.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	r.RemoteAddr = "8.8.8.8:12345"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("empty allowlist -> %d, want 200", w.Code)
	}
}

func TestInvalidCIDRRejected(t *testing.T) {
	t.Parallel()
	if _, err := ParseIPAllowlist("not-a-cidr"); err == nil {
		t.Fatal("invalid CIDR should be rejected")
	}
}
