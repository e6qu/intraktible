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

// TestUnderscoreAssetsAreEmbedded guards the //go:embed all: directive: SvelteKit
// emits JS/CSS under _app/, and a bare //go:embed would silently drop it (blank
// UI). The committed _app/embed-probe.txt is served as itself (not the SPA shell)
// only when _app is embedded.
func TestUnderscoreAssetsAreEmbedded(t *testing.T) {
	srv := httptest.NewServer(web.Handler())
	defer srv.Close()

	_, shell := get(t, srv, "/")
	status, body := get(t, srv, "/_app/embed-probe.txt")
	if status != http.StatusOK {
		t.Fatalf("GET /_app/embed-probe.txt -> %d, want 200 (is //go:embed missing all:?)", status)
	}
	if body == shell {
		t.Fatal("_app/ asset fell back to the index shell — //go:embed dropped underscore files; use all:assets")
	}
}
