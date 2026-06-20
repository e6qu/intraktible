// SPDX-License-Identifier: AGPL-3.0-or-later

package scim

import (
	"encoding/json"
	"testing"
)

// FuzzUserNameFilter asserts the SCIM filter parser never panics on an arbitrary
// IdP-supplied `filter` query value (it does index math on quote positions), and
// that any value it extracts is genuinely a substring of the input (it must not
// fabricate a userName the deprovisioning gate would then match against).
func FuzzUserNameFilter(f *testing.F) {
	for _, s := range []string{
		`userName eq "ada@acme.com"`,
		`userName eq "B Jensen"`,
		`userName eq "`,
		`"`,
		``,
		`userName eq`,
		`emails eq "x"`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, filter string) {
		got, _ := userNameFilter(filter) // must not panic
		if got != "" && !containsSub(filter, got) {
			t.Fatalf("userNameFilter(%q) = %q, which is not a substring of the input", filter, got)
		}
	})
}

func containsSub(haystack, needle string) bool {
	return len(needle) <= len(haystack) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

// FuzzParseBool asserts the SCIM PatchOp bool parser never panics on arbitrary
// JSON (Azure sends bool, Okta sends string-encoded bools).
func FuzzParseBool(f *testing.F) {
	for _, s := range []string{`true`, `false`, `"True"`, `"0"`, `null`, `{}`, `[1,2]`, `"yes"`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if !json.Valid([]byte(raw)) {
			return
		}
		_, _ = parseBool(json.RawMessage(raw)) // must not panic
	})
}
