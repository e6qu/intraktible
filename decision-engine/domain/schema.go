// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// inputSchema is the supported subset of JSON Schema for a flow's decide contract:
// an object with required keys and per-property types. Other keywords (nested
// schemas, $ref, allOf, …) are accepted but not enforced — the validator is
// lenient on what it does not understand, so it never rejects an otherwise-valid
// input. (Tracked as a known limitation; see BUGS.)
type inputSchema struct {
	Type       string `json:"type"`
	Required   []string
	Properties map[string]struct {
		Type string `json:"type"`
	}
}

// ValidateInput checks data against a flow version's input schema. An empty schema
// is no contract (nil). A schema that is present but not a JSON object is a broken
// contract and fails loudly. Otherwise it enforces the declared required keys and
// the declared property types.
func ValidateInput(schema json.RawMessage, data map[string]any) error {
	if len(schema) == 0 {
		return nil
	}
	var s inputSchema
	if err := json.Unmarshal(schema, &s); err != nil {
		return fmt.Errorf("decision-engine: input_schema is not a valid object schema: %w", err)
	}
	for _, name := range s.Required {
		if _, ok := data[name]; !ok {
			return fmt.Errorf("decision-engine: missing required input %q", name)
		}
	}
	// Check declared property types in a stable order (deterministic error).
	names := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		v, present := data[name]
		if !present {
			continue
		}
		if t := s.Properties[name].Type; t != "" && !typeMatches(t, v) {
			return fmt.Errorf("decision-engine: input %q must be %s", name, t)
		}
	}
	return nil
}

// typeMatches reports whether v (a value decoded from JSON into Go's any) satisfies
// the JSON Schema type t. Unknown types are accepted (lenient).
func typeMatches(t string, v any) bool {
	switch t {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == math.Trunc(f)
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "null":
		return v == nil
	default:
		return true
	}
}
