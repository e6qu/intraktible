// SPDX-License-Identifier: AGPL-3.0-or-later

// Package openapi serves the public data-plane API contract as an OpenAPI 3.1
// document, plus a dependency-free reference page that renders it. The document
// is the artifact integrators point their tooling (codegen, Swagger UI, Postman)
// at; it is embedded in the binary so a running instance always serves its own
// contract.
package openapi

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
)

//go:embed openapi.json
var spec []byte

//go:embed docs.html
var docsPage []byte

// Doc is the minimal shape we parse out of the embedded spec for validation and
// introspection (the document served to clients is the raw bytes).
type Doc struct {
	OpenAPI string `json:"openapi"`
	Info    struct {
		Title   string `json:"title"`
		Version string `json:"version"`
	} `json:"info"`
	Paths map[string]map[string]any `json:"paths"`
}

// Parse decodes the embedded spec. It fails loudly if the embedded document is
// not valid JSON — a build-time guarantee surfaced as a startup/test error.
func Parse() (Doc, error) {
	var d Doc
	if err := json.Unmarshal(spec, &d); err != nil {
		return Doc{}, fmt.Errorf("openapi: invalid embedded spec: %w", err)
	}
	return d, nil
}

// Spec returns the raw OpenAPI document bytes.
func Spec() []byte { return spec }

// Routes registers the public contract endpoints: the document and a reference
// page. Both are unauthenticated — the contract is meant to be fetchable by any
// integrator or code generator.
func Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /openapi.json", serveSpec)
	mux.HandleFunc("GET /docs", serveDocs)
}

func serveSpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(Spec())
}

func serveDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(docsPage)
}
