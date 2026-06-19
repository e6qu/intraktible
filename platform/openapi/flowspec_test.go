// SPDX-License-Identifier: AGPL-3.0-or-later

package openapi_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/platform/openapi"
)

func TestForFlowEmbedsInputSchema(t *testing.T) {
	in := json.RawMessage(`{"type":"object","required":["fico"],"properties":{"fico":{"type":"integer"}}}`)
	doc, err := openapi.ForFlow("credit-score", "Credit Score", in)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatalf("generated doc is not valid JSON: %v", err)
	}
	if parsed.OpenAPI != "3.1.0" {
		t.Fatalf("openapi version = %q, want 3.1.0", parsed.OpenAPI)
	}
	if _, ok := parsed.Paths["/v1/flows/credit-score/{env}/decide"]; !ok {
		t.Fatalf("missing per-flow decide path; paths = %v", parsed.Paths)
	}
	if _, ok := parsed.Paths["/v1/flows/credit-score/{env}/decide/batch"]; !ok {
		t.Fatalf("missing per-flow batch path")
	}
	// The published input schema must flow into the request `data` schema.
	if !strings.Contains(string(doc), `"fico"`) {
		t.Fatalf("input schema not embedded in the contract:\n%s", doc)
	}
}

func TestForFlowWithoutSchema(t *testing.T) {
	doc, err := openapi.ForFlow("bare", "Bare", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(doc) {
		t.Fatal("generated doc is not valid JSON")
	}
}
