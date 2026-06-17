// SPDX-License-Identifier: AGPL-3.0-or-later

// Package privacy is the platform's field-level masking: a per-workspace set of
// sensitive field names and a pure masker that redacts their values in JSON
// payloads at the read boundary. Like connector credential redaction, the raw
// data stays in the event log (the source of truth) — only what is served over
// HTTP is masked, so a decision's PII never reaches a viewer's screen or an export.
package privacy

import (
	"encoding/json"
	"strings"
)

// Redacted is the placeholder substituted for a masked field's value.
const Redacted = "[redacted]"

// FieldSet builds a case-insensitive lookup from a list of field names.
func FieldSet(fields []string) map[string]bool {
	out := make(map[string]bool, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(strings.ToLower(f)); f != "" {
			out[f] = true
		}
	}
	return out
}

// Mask returns a copy of raw with the value of any key whose (case-insensitive)
// name is in fields replaced by Redacted, recursing into nested objects and
// arrays. Empty fields, empty input, or unparseable JSON is returned unchanged.
func Mask(raw json.RawMessage, fields map[string]bool) json.RawMessage {
	if len(raw) == 0 || len(fields) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.Marshal(maskValue(v, fields))
	if err != nil {
		return raw
	}
	return out
}

func maskValue(v any, fields map[string]bool) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if fields[strings.ToLower(k)] {
				t[k] = Redacted
			} else {
				t[k] = maskValue(val, fields)
			}
		}
		return t
	case []any:
		for i := range t {
			t[i] = maskValue(t[i], fields)
		}
		return t
	default:
		return v
	}
}
