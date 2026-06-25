// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"testing"
	"time"
)

// FuzzWebhookTemplate asserts the webhook message template path never panics or
// hangs on an arbitrary operator-supplied template. The template arrives via the
// /subscribe API and is then executed against an alert payload, so a malformed or
// adversarial template must surface as an error, not a crash, runaway, or hang.
func FuzzWebhookTemplate(f *testing.F) {
	for _, s := range []string{
		``,
		`{{.Reason}}`,
		`{{.Missing}}`,
		`{{`,
		`{{.}}`,
		`{{range .Items}}{{.}}{{end}}`,
		`{{define "x"}}{{template "x"}}{{end}}{{template "x"}}`,
		`{{printf "%v" .}}`,
		`{{if .X}}{{.Y}}{{end}}`,
		`{{.Reason | printf "%999999999d"}}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, tmpl string) {
		if len(tmpl) > 64<<10 {
			return
		}
		// validateTemplate must never panic (it is the subscribe-time gate).
		_ = validateTemplate(tmpl)

		data := map[string]any{
			"Reason":  "monitor check",
			"Flow":    "loan-approval",
			"Details": map[string]any{"score": 42, "tags": []string{"a", "b"}},
			"Items":   []any{1, "two", map[string]any{"k": "v"}},
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = renderTemplate(tmpl, data) // must not panic
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("renderTemplate did not return within 10s on template %q (runaway execution?)", tmpl)
		}
	})
}
