// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// validateTemplate parses a webhook message template, returning a loud error for a
// malformed one (checked at subscribe time). An empty template is valid (the raw
// JSON payload is sent).
func validateTemplate(tmpl string) error {
	if tmpl == "" {
		return nil
	}
	if _, err := template.New("webhook").Parse(tmpl); err != nil {
		return fmt.Errorf("notify: invalid webhook template: %w", err)
	}
	return nil
}

// renderTemplate executes a webhook message template against the alert payload
// (decoded to a generic value), producing the per-channel request body.
func renderTemplate(tmpl string, data any) ([]byte, error) {
	t, err := template.New("webhook").Option("missingkey=zero").Parse(tmpl)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// wantsReason reports whether a webhook with the given event filter accepts a
// delivery for reason. An empty filter accepts everything; otherwise any filter
// token that is a case-insensitive substring of the reason routes the delivery
// (so "monitor" catches both "monitor check" and "monitor scheduler").
func wantsReason(events []string, reason string) bool {
	if len(events) == 0 {
		return true
	}
	r := strings.ToLower(reason)
	for _, e := range events {
		if tok := strings.ToLower(strings.TrimSpace(e)); tok != "" && strings.Contains(r, tok) {
			return true
		}
	}
	return false
}
