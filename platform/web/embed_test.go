// SPDX-License-Identifier: AGPL-3.0-or-later

package web_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e6qu/intraktible/platform/web"
)

func get(t *testing.T, srv *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func TestHandlerServesIndexWithSPAFallback(t *testing.T) {
	srv := httptest.NewServer(web.Handler())
	defer srv.Close()

	status, body := get(t, srv, "/")
	if status != http.StatusOK {
		t.Fatalf("GET / -> %d, want 200", status)
	}
	if body == "" {
		t.Fatal("GET / served an empty body")
	}

	// SPA fallback: an unknown (client-side) route returns the index shell with a
	// 200 — not a 404 — so deep links load the app rather than failing.
	clientStatus, clientBody := get(t, srv, "/engine")
	if clientStatus != http.StatusOK {
		t.Fatalf("GET /engine -> %d, want 200 (SPA fallback)", clientStatus)
	}
	if clientBody != body {
		t.Fatal("GET /engine should fall back to the same index shell as /")
	}
}
