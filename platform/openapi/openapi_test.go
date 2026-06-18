// SPDX-License-Identifier: AGPL-3.0-or-later

package openapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e6qu/intraktible/platform/openapi"
)

func TestSpecIsValidOpenAPI(t *testing.T) {
	doc, err := openapi.Parse()
	if err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Fatalf("openapi version = %q, want 3.1.0", doc.OpenAPI)
	}
	if doc.Info.Title == "" || doc.Info.Version == "" {
		t.Fatalf("info must carry a title and version: %+v", doc.Info)
	}
}

func TestCorePathsAndMethodsDocumented(t *testing.T) {
	doc, err := openapi.Parse()
	if err != nil {
		t.Fatal(err)
	}
	// The data-plane integration surface external callers depend on.
	want := map[string]string{
		"/v1/flows/{slug}/{env}/decide":   "post",
		"/v1/decisions":                   "get",
		"/v1/decisions/{decision_id}":     "get",
		"/v1/flows":                       "post",
		"/v1/flows/import":                "post",
		"/v1/flows/import-bundle":         "post",
		"/v1/flows/{flow_id}/deployments": "post",
		"/v1/flows/{flow_id}/promote":     "post",
		"/v1/me":                          "get",
	}
	for path, method := range want {
		ops, ok := doc.Paths[path]
		if !ok {
			t.Fatalf("spec is missing core path %q", path)
		}
		if _, ok := ops[method]; !ok {
			t.Fatalf("path %q is missing the %q method", path, method)
		}
	}
	// Every documented operation must use a real HTTP method (internal consistency).
	valid := map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true, "parameters": true}
	for path, ops := range doc.Paths {
		for method := range ops {
			if !valid[method] {
				t.Fatalf("path %q has unexpected method key %q", path, method)
			}
		}
	}
}

func TestServeSpecAndDocs(t *testing.T) {
	mux := http.NewServeMux()
	openapi.Routes(mux)

	specRec := httptest.NewRecorder()
	mux.ServeHTTP(specRec, httptest.NewRequest(http.MethodGet, "/openapi.json", http.NoBody))
	if specRec.Code != http.StatusOK {
		t.Fatalf("GET /openapi.json -> %d", specRec.Code)
	}
	if ct := specRec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("spec content-type = %q", ct)
	}
	var parsed map[string]any
	if err := json.Unmarshal(specRec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("served spec is not valid JSON: %v", err)
	}

	docsRec := httptest.NewRecorder()
	mux.ServeHTTP(docsRec, httptest.NewRequest(http.MethodGet, "/docs", http.NoBody))
	if docsRec.Code != http.StatusOK {
		t.Fatalf("GET /docs -> %d", docsRec.Code)
	}
	if ct := docsRec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("docs content-type = %q", ct)
	}
}
