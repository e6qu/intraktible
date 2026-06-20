// SPDX-License-Identifier: AGPL-3.0-or-later

package schema_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/platform/schema"
)

// FuzzValidateObject asserts the recursive JSON-Schema validator never panics on
// arbitrary schema + data — it must always return (a possibly-error) result, even
// for adversarial input ($ref cycles, deep nesting, type confusion). It is the
// untrusted boundary for decide-input contracts and agent structured output.
func FuzzValidateObject(f *testing.F) {
	f.Add(`{"type":"object","required":["x"],"properties":{"x":{"type":"number"}}}`, `{"x":1}`)
	f.Add(`{"$defs":{"a":{"$ref":"#/$defs/a"}},"properties":{"y":{"$ref":"#/$defs/a"}}}`, `{"y":1}`)
	f.Add(`{"allOf":[{"type":"object"}],"additionalProperties":false}`, `{"z":true}`)
	f.Add(`{"type":["object","null"],"properties":{"n":{"type":"array","items":{"type":"string"}}}}`, `{"n":["a","b"]}`)
	f.Add(`{}`, `{}`)

	f.Fuzz(func(t *testing.T, schemaJSON, dataJSON string) {
		if !json.Valid([]byte(schemaJSON)) || !json.Valid([]byte(dataJSON)) {
			return
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
			return // not an object — ValidateObject takes a map
		}
		// Must return, never panic (a $ref cycle is depth-bounded, not a stack overflow).
		_ = schema.ValidateObject(json.RawMessage(schemaJSON), data)
	})
}
