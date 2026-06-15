// SPDX-License-Identifier: AGPL-3.0-or-later

package schema_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/platform/schema"
)

func TestValidateObject(t *testing.T) {
	doc := json.RawMessage(`{
		"type": "object",
		"required": ["fico", "name"],
		"properties": {
			"fico": {"type": "integer"},
			"name": {"type": "string"},
			"active": {"type": "boolean"}
		}
	}`)

	ok := []map[string]any{
		{"fico": 700.0, "name": "ada"},
		{"fico": 700.0, "name": "ada", "active": true},
		{"fico": 700.0, "name": "ada", "extra": "free"},
	}
	for i, d := range ok {
		if err := schema.ValidateObject(doc, d); err != nil {
			t.Fatalf("valid %d rejected: %v", i, err)
		}
	}

	bad := []map[string]any{
		{"name": "ada"},                                 // missing required fico
		{"fico": 700.0},                                 // missing required name
		{"fico": "high", "name": "ada"},                 // fico not integer
		{"fico": 700.5, "name": "ada"},                  // fico not whole
		{"fico": 700.0, "name": 42.0},                   // name not string
		{"fico": 700.0, "name": "ada", "active": "yes"}, // active not boolean
	}
	for i, d := range bad {
		if err := schema.ValidateObject(doc, d); err == nil {
			t.Fatalf("bad %d accepted: %+v", i, d)
		}
	}
}

func TestValidateObjectEmptyAndBroken(t *testing.T) {
	if err := schema.ValidateObject(nil, map[string]any{"x": 1}); err != nil {
		t.Fatalf("empty schema should not validate: %v", err)
	}
	if err := schema.ValidateObject(json.RawMessage(`"nope"`), map[string]any{}); err == nil {
		t.Fatal("a non-object schema should fail loudly")
	}
}
