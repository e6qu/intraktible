// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/platform/httpx"
)

func TestJSON(t *testing.T) {
	w := httptest.NewRecorder()
	httpx.JSON(w, http.StatusTeapot, map[string]int{"n": 1})
	if w.Code != http.StatusTeapot {
		t.Fatalf("code = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"n":1}` {
		t.Fatalf("body = %q", got)
	}
}

func TestError(t *testing.T) {
	w := httptest.NewRecorder()
	httpx.Error(w, http.StatusBadRequest, errors.New("boom"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d", w.Code)
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"error":"boom"}` {
		t.Fatalf("body = %q", got)
	}
}

func TestDecodeJSON(t *testing.T) {
	type body struct {
		Name string `json:"name"`
	}
	var v body
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ada"}`))
	if err := httpx.DecodeJSON(r, &v); err != nil || v.Name != "ada" {
		t.Fatalf("decode: v=%+v err=%v", v, err)
	}
	// Unknown fields are rejected (strict decode).
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ada","extra":1}`))
	if err := httpx.DecodeJSON(r, &v); err == nil {
		t.Fatal("expected unknown-field rejection")
	}
	// Trailing data after the first JSON value is rejected — a second object can't
	// be smuggled past the strict decode.
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ada"}{"name":"eve"}`))
	if err := httpx.DecodeJSON(r, &v); err == nil {
		t.Fatal("expected trailing-data rejection")
	}
	// Trailing whitespace is fine (a normal newline-terminated body).
	r = httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{\"name\":\"ada\"}\n"))
	if err := httpx.DecodeJSON(r, &v); err != nil {
		t.Fatalf("trailing whitespace should decode: %v", err)
	}
}

func TestVersionHandler(t *testing.T) {
	w := httptest.NewRecorder()
	httpx.Version()(w, httptest.NewRequest(http.MethodGet, "/version", http.NoBody))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var metadata map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &metadata); err != nil {
		t.Fatalf("decode version body: %v", err)
	}
	if metadata["service"] != "intraktible" || metadata["revision"] == "" || metadata["go"] == "" {
		t.Fatalf("incomplete version metadata: %#v", metadata)
	}
	if _, ok := metadata["built_at"]; !ok {
		t.Fatalf("version metadata missing built_at: %#v", metadata)
	}
}

func TestDecodeJSONRejectsOversizedBody(t *testing.T) {
	// A JSON string just past the cap must be rejected (guards against abusive bodies).
	big := `"` + strings.Repeat("a", httpx.MaxJSONBody+16) + `"`
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(big))
	var v string
	if err := httpx.DecodeJSON(r, &v); err == nil {
		t.Fatal("expected an oversized body to be rejected")
	}

	// A small body still decodes fine.
	r2 := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`"ok"`))
	var v2 string
	if err := httpx.DecodeJSON(r2, &v2); err != nil || v2 != "ok" {
		t.Fatalf("small body decode: v=%q err=%v", v2, err)
	}
}

func TestDownloadSanitizesFilename(t *testing.T) {
	rec := httptest.NewRecorder()
	// A hostile filename with CRLF + a quote that would otherwise break out of the
	// quoted Content-Disposition and inject a header / split the response.
	httpx.Download(rec, "text/csv", "a\"\r\nSet-Cookie: x=1.csv", "body")
	cd := rec.Header().Get("Content-Disposition")
	if strings.ContainsAny(cd, "\r\n\"") && !strings.HasPrefix(cd, `attachment; filename="`) {
		t.Fatalf("unexpected header shape: %q", cd)
	}
	if strings.Contains(cd, "\r") || strings.Contains(cd, "\n") {
		t.Fatalf("Content-Disposition must not contain CR/LF: %q", cd)
	}
	// The injected quote must be neutralized (no second quoted segment / header).
	if strings.Contains(cd, "Set-Cookie") && strings.Contains(cd, "\n") {
		t.Fatalf("header injection not prevented: %q", cd)
	}
}
