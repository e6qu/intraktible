// SPDX-License-Identifier: AGPL-3.0-or-later

// Package notifications is a per-user inbox derived from @-mentions in comments.
// A projector folds the comment stream: every "@handle" in a comment body becomes
// a notification for that recipient, who can list and mark them read. The inbox is
// a read model over events, so it is durable and rebuilds on replay.
package notifications

import "regexp"

// mentionRe matches "@handle" at the start or after whitespace, so an email like
// "a@b.com" is not mistaken for a mention. A literal pattern (so the
// detect-non-literal-regexp lint is satisfied).
var mentionRe = regexp.MustCompile(`(?:^|\s)@([A-Za-z0-9_.-]+)`)

// ParseMentions extracts the unique @-mentioned handles from a comment body, in
// first-seen order.
func ParseMentions(body string) []string {
	matches := mentionRe.FindAllStringSubmatch(body, -1)
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		h := m[1]
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}
