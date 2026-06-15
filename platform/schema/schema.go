// SPDX-License-Identifier: AGPL-3.0-or-later

// Package schema validates JSON documents against a supported subset of JSON
// Schema — an object with required keys and per-property types. It is shared by
// the decision engine (decide-input contracts) and the Agent Manager (agent
// structured output). Keywords it does not understand are accepted, so it never
// rejects an otherwise-valid document; the unenforced keywords are a known limit.
package schema

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

type objectSchema struct {
	Type       string `json:"type"`
	Required   []string
	Properties map[string]struct {
		Type string `json:"type"`
	}
}

// ValidateObject checks data against schema. An empty schema is no contract. A
// schema that is present but not a JSON object is a broken contract and fails
// loudly. Otherwise it enforces the declared required keys and property types.
func ValidateObject(schema json.RawMessage, data map[string]any) error {
	if len(schema) == 0 {
		return nil
	}
	var s objectSchema
	if err := json.Unmarshal(schema, &s); err != nil {
		return fmt.Errorf("schema: not a valid object schema: %w", err)
	}
	for _, name := range s.Required {
		if _, ok := data[name]; !ok {
			return fmt.Errorf("schema: missing required field %q", name)
		}
	}
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
			return fmt.Errorf("schema: field %q must be %s", name, t)
		}
	}
	return nil
}

// typeMatches reports whether v (decoded from JSON into Go's any) satisfies the
// JSON Schema type t. Unknown types are accepted (lenient).
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
