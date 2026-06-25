// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/e6qu/intraktible/platform/notifications"
)

// FuzzParseMentions asserts the @-mention extractor stays well-behaved on an
// arbitrary comment body (it is fed raw user-authored text): it never panics, every
// returned handle is non-empty, de-duplicated, free of the leading @, and a literal
// substring of the body — so the projector can address a notification to it safely.
func FuzzParseMentions(f *testing.F) {
	seeds := []string{
		"hi @alice and @bob",
		"email a@b.com is not a mention",
		"@a @a @b repeated",
		"@@@@ weird",
		"no mentions here",
		"@unicodé_handle.dash-ok mid@line @start",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		got := notifications.ParseMentions(body)
		seen := make(map[string]bool, len(got))
		for _, h := range got {
			if h == "" {
				t.Fatalf("empty handle from %q", body)
			}
			if !utf8.ValidString(h) {
				t.Fatalf("non-utf8 handle %q from %q", h, body)
			}
			if strings.HasPrefix(h, "@") {
				t.Fatalf("handle %q still carries leading @ (body %q)", h, body)
			}
			if !strings.Contains(body, h) {
				t.Fatalf("handle %q is not a substring of body %q", h, body)
			}
			if seen[h] {
				t.Fatalf("duplicate handle %q in result for %q", h, body)
			}
			seen[h] = true
		}
	})
}
