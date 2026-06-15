// SPDX-License-Identifier: AGPL-3.0-or-later

package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e6qu/intraktible/platform/web"
)

func TestHandlerServesIndex(t *testing.T) {
	srv := httptest.NewServer(web.Handler())
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / -> %d, want 200", resp.StatusCode)
	}

	missing, err := srv.Client().Get(srv.URL + "/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = missing.Body.Close() }()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /does-not-exist -> %d, want 404", missing.StatusCode)
	}
}
