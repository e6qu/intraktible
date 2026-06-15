// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx_test

import (
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
}
