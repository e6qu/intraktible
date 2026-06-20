// SPDX-License-Identifier: AGPL-3.0-or-later

package scim

import "testing"

func TestUserNameFilter(t *testing.T) {
	cases := []struct {
		filter      string
		want        string
		wantPresent bool
	}{
		{`userName eq "bjensen"`, "bjensen", true},
		{`userName eq "ada@acme.com"`, "ada@acme.com", true},
		{`userName eq "B Jensen"`, "B Jensen", true},     // value with a space must survive
		{`  userName   eq   "spaced"  `, "spaced", true}, // tolerant of surrounding/inner whitespace
		{`USERNAME EQ "case"`, "case", true},             // attribute/operator are case-insensitive
		{`userName co "partial"`, "", false},             // unsupported operator
		{`displayName eq "x"`, "", false},                // unsupported attribute
		{`userName eq bjensen`, "", false},               // unquoted value
		{``, "", false},                                  // no filter param → list all
		{`userName eq ""`, "", true},                     // present-but-empty: a precise filter (matches no user), NOT "list all"
	}
	for _, c := range cases {
		if got, present := userNameFilter(c.filter); got != c.want || present != c.wantPresent {
			t.Errorf("userNameFilter(%q) = (%q,%v), want (%q,%v)", c.filter, got, present, c.want, c.wantPresent)
		}
	}
}
