// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/domain"
)

func TestValidateInput(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["fico", "name"],
		"properties": {
			"fico": {"type": "integer"},
			"name": {"type": "string"},
			"active": {"type": "boolean"}
		}
	}`)

	ok := []map[string]any{
		{"fico": 700.0, "name": "ada"},                  // required present, right types
		{"fico": 700.0, "name": "ada", "active": true},  // optional property, right type
		{"fico": 700.0, "name": "ada", "extra": "free"}, // unknown property is allowed
	}
	for i, d := range ok {
		if err := domain.ValidateInput(schema, d); err != nil {
			t.Fatalf("valid %d rejected: %v", i, err)
		}
	}

	bad := []map[string]any{
		{"name": "ada"},                                 // missing required fico
		{"fico": 700.0},                                 // missing required name
		{"fico": "high", "name": "ada"},                 // fico not an integer
		{"fico": 700.5, "name": "ada"},                  // fico not a whole number
		{"fico": 700.0, "name": 42.0},                   // name not a string
		{"fico": 700.0, "name": "ada", "active": "yes"}, // active not a boolean
	}
	for i, d := range bad {
		if err := domain.ValidateInput(schema, d); err == nil {
			t.Fatalf("bad %d accepted: %+v", i, d)
		}
	}
}

func TestValidateInputEmptyAndBroken(t *testing.T) {
	// No schema => no contract.
	if err := domain.ValidateInput(nil, map[string]any{"x": 1}); err != nil {
		t.Fatalf("empty schema should not validate: %v", err)
	}
	// A present-but-non-object schema is a broken contract.
	if err := domain.ValidateInput(json.RawMessage(`"nope"`), map[string]any{}); err == nil {
		t.Fatal("a non-object schema should fail loudly")
	}
}
