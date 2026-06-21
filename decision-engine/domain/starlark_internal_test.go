// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"
	"reflect"
	"testing"
)

// FuzzStarlarkRoundTrip asserts the Code-node JSON⇄Starlark conversion never panics
// on arbitrary (JSON-shaped) values and is a FIXPOINT after the first pass. The first
// toStarlark is intentionally lossy (an integral float like 1.0 becomes an int, a
// huge int becomes a decimal string), so equality is asserted on the SECOND
// round-trip — catching genuine instability while tolerating the documented coercion.
func FuzzStarlarkRoundTrip(f *testing.F) {
	f.Add(`{"a":1,"b":[1,2.5,"x",true,null],"c":{"d":1.0}}`)
	f.Add(`[1e308, 9223372036854775808, -0.0]`)
	f.Add(`"plain string"`)
	f.Add(`{"nested":{"nested":{"nested":[1]}}}`)
	f.Fuzz(func(t *testing.T, jsonStr string) {
		if !json.Valid([]byte(jsonStr)) {
			return
		}
		var v any
		if err := json.Unmarshal([]byte(jsonStr), &v); err != nil {
			return
		}
		sv, err := toStarlark(v) // must not panic
		if err != nil {
			return
		}
		first, ok := fromStarlark(sv)
		if !ok {
			return
		}
		// Second pass must be a no-op: toStarlark(first) -> fromStarlark -> first.
		sv2, err := toStarlark(first)
		if err != nil {
			t.Fatalf("re-converting a fromStarlark result failed: %v", err)
		}
		second, ok := fromStarlark(sv2)
		if !ok {
			t.Fatalf("re-converting a toStarlark result was not representable")
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("Starlark round-trip is not a fixpoint:\n first:  %#v\n second: %#v", first, second)
		}
	})
}
