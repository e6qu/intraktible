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

// accepts/rejects runs a table of documents against one schema.
func check(t *testing.T, name string, doc json.RawMessage, ok, bad []map[string]any) {
	t.Helper()
	for i, d := range ok {
		if err := schema.ValidateObject(doc, d); err != nil {
			t.Fatalf("%s: valid %d rejected: %v", name, i, err)
		}
	}
	for i, d := range bad {
		if err := schema.ValidateObject(doc, d); err == nil {
			t.Fatalf("%s: bad %d accepted: %+v", name, i, d)
		}
	}
}

func TestNumericAndStringConstraints(t *testing.T) {
	doc := json.RawMessage(`{
		"type": "object",
		"properties": {
			"score":  {"type": "integer", "minimum": 0, "maximum": 100},
			"rate":   {"type": "number", "exclusiveMinimum": 0, "multipleOf": 0.5},
			"name":   {"type": "string", "minLength": 2, "maxLength": 5, "pattern": "^[a-z]+$"},
			"email":  {"type": "string", "format": "email"}
		}
	}`)
	check(t, "constraints", doc,
		[]map[string]any{
			{"score": 100.0, "rate": 2.5, "name": "ada", "email": "a@b.co"},
			{"score": 0.0},
		},
		[]map[string]any{
			{"score": 101.0},          // > maximum
			{"score": -1.0},           // < minimum
			{"rate": 0.0},             // not > exclusiveMinimum
			{"rate": 0.3},             // not a multiple of 0.5
			{"name": "a"},             // shorter than minLength
			{"name": "abcdef"},        // longer than maxLength
			{"name": "Ada"},           // fails pattern
			{"email": "not-an-email"}, // fails format
		},
	)
}

func TestEnumConstAndNested(t *testing.T) {
	doc := json.RawMessage(`{
		"type": "object",
		"required": ["status", "address"],
		"properties": {
			"status":  {"enum": ["approved", "declined", "review"]},
			"version": {"const": 2},
			"address": {
				"type": "object",
				"required": ["zip"],
				"properties": { "zip": {"type": "string", "pattern": "^[0-9]{5}$"} },
				"additionalProperties": false
			},
			"tags": {"type": "array", "items": {"type": "string"}, "uniqueItems": true, "minItems": 1}
		}
	}`)
	check(t, "enum/const/nested", doc,
		[]map[string]any{
			{"status": "approved", "address": map[string]any{"zip": "94016"}},
			{"status": "review", "version": 2.0, "address": map[string]any{"zip": "10001"}, "tags": []any{"a", "b"}},
		},
		[]map[string]any{
			{"status": "maybe", "address": map[string]any{"zip": "94016"}},                    // bad enum
			{"status": "approved", "version": 3.0, "address": map[string]any{"zip": "94016"}}, // bad const
			{"status": "approved", "address": map[string]any{"zip": "abc"}},                   // nested pattern
			{"status": "approved", "address": map[string]any{"zip": "94016", "extra": "x"}},   // additionalProperties:false
			{"status": "approved"}, // missing required address
			{"status": "approved", "address": map[string]any{"zip": "94016"}, "tags": []any{}},         // minItems
			{"status": "approved", "address": map[string]any{"zip": "94016"}, "tags": []any{"a", "a"}}, // uniqueItems
		},
	)
}

func TestRefAndCombinators(t *testing.T) {
	doc := json.RawMessage(`{
		"type": "object",
		"$defs": { "money": {"type": "number", "minimum": 0} },
		"required": ["amount", "currency"],
		"properties": {
			"amount":   {"$ref": "#/$defs/money"},
			"currency": {"anyOf": [{"const": "USD"}, {"const": "EUR"}]}
		}
	}`)
	check(t, "ref/combinators", doc,
		[]map[string]any{
			{"amount": 10.0, "currency": "USD"},
			{"amount": 0.0, "currency": "EUR"},
		},
		[]map[string]any{
			{"amount": -1.0, "currency": "USD"}, // $ref minimum
			{"amount": 10.0, "currency": "GBP"}, // anyOf
		},
	)
}
