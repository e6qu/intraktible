// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"encoding/json"
	"testing"
)

// labelFromSealed must pass a plain string through but never surface a sealed PII
// envelope (an object) as cleartext in the escalation event — it becomes a
// placeholder so the manual_review case label is crypto-shred-consistent with the
// rest of the recorded decision.
func TestLabelFromSealed(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"plain string", `"Acme Corp"`, "Acme Corp"},
		{"empty string", `""`, ""},
		{"absent", ``, ""},
		{"null", `null`, ""},
		{"sealed envelope", `{"$intraktible_sealed":"v1","value":"x"}`, "[sealed]"},
		{"erased envelope", `{"$intraktible_erased":"v1","value":"x"}`, "[sealed]"},
	}
	for _, c := range cases {
		if got := labelFromSealed(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("%s: labelFromSealed(%q) = %q, want %q", c.name, c.raw, got, c.want)
		}
	}
}
