// SPDX-License-Identifier: AGPL-3.0-or-later

package scim

import "testing"

func TestUserNameFilter(t *testing.T) {
	cases := []struct {
		filter string
		want   string
	}{
		{`userName eq "bjensen"`, "bjensen"},
		{`userName eq "ada@acme.com"`, "ada@acme.com"},
		{`userName eq "B Jensen"`, "B Jensen"},     // value with a space must survive
		{`  userName   eq   "spaced"  `, "spaced"}, // tolerant of surrounding/inner whitespace
		{`USERNAME EQ "case"`, "case"},             // attribute/operator are case-insensitive
		{`userName co "partial"`, ""},              // unsupported operator
		{`displayName eq "x"`, ""},                 // unsupported attribute
		{`userName eq bjensen`, ""},                // unquoted value
		{``, ""},
		{`userName eq ""`, ""}, // empty quoted value → no filter
	}
	for _, c := range cases {
		if got := userNameFilter(c.filter); got != c.want {
			t.Errorf("userNameFilter(%q) = %q, want %q", c.filter, got, c.want)
		}
	}
}
