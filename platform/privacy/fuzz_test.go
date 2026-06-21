// SPDX-License-Identifier: AGPL-3.0-or-later

package privacy_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/platform/privacy"
)

// FuzzMask asserts the PII masker — a security boundary keeping sensitive fields off
// a viewer's screen/export — is robust and COMPLETE: it never panics on adversarial
// (deeply nested) JSON, its output always re-parses, every field named in the set is
// redacted at ANY depth, and masking is idempotent.
func FuzzMask(f *testing.F) {
	f.Add(`{"ssn":"123","nested":{"ssn":"456","ok":"keep"}}`, "ssn")
	f.Add(`{"a":[{"email":"x@y"},{"email":"z@w"}]}`, "email")
	f.Add(`{"SSN":"upper"}`, "ssn")
	f.Add(`[{"pwd":"p"},1,"two",null]`, "pwd,token")
	f.Fuzz(func(t *testing.T, rawJSON, fieldsCSV string) {
		if !json.Valid([]byte(rawJSON)) {
			return
		}
		fields := privacy.FieldSet(strings.Split(fieldsCSV, ","))
		out := privacy.Mask(json.RawMessage(rawJSON), fields) // must not panic
		if len(fields) == 0 {
			return // no-op path
		}
		if !json.Valid([]byte(out)) {
			t.Fatalf("masked output is not valid JSON: %s", out)
		}
		var v any
		if err := json.Unmarshal(out, &v); err != nil {
			t.Fatal(err)
		}
		assertRedacted(t, v, fields)
		// Idempotent: masking already-masked output changes nothing.
		out2 := privacy.Mask(out, fields)
		if string(out2) != string(out) {
			t.Fatalf("Mask is not idempotent:\n first:  %s\n second: %s", out, out2)
		}
	})
}

// assertRedacted walks a decoded JSON value and fails if any key named in fields
// carries a value other than the redaction placeholder.
func assertRedacted(t *testing.T, v any, fields map[string]bool) {
	switch m := v.(type) {
	case map[string]any:
		for k, val := range m {
			if fields[strings.ToLower(k)] {
				if val != privacy.Redacted {
					t.Fatalf("field %q not redacted: %#v", k, val)
				}
				continue // a redacted value is a leaf string — nothing to recurse into
			}
			assertRedacted(t, val, fields)
		}
	case []any:
		for _, e := range m {
			assertRedacted(t, e, fields)
		}
	}
}
