// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
)

// FuzzValidateFlow asserts publish-time validation never panics on an arbitrary
// graph — it decodes every node's config and dry-compiles expr-lang + Starlark, so
// a malformed config/expression/script must return an error, never crash.
func FuzzValidateFlow(f *testing.F) {
	seeds := []string{
		`{"nodes":[{"id":"in","type":"input"},{"id":"r","type":"rule","config":{"rules":[{"when":"x>1","then":[{"target":"y","expr":"1"}]}]}},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"r"},{"from":"r","to":"o"}]}`,
		`{"nodes":[{"id":"in","type":"input"},{"id":"c","type":"code","config":{"code":"y = 1"}},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"c"},{"from":"c","to":"o"}]}`,
		`{"nodes":[{"id":"in","type":"input"},{"id":"s","type":"split","config":{"condition":"a &&"}},{"id":"o","type":"output"}]}`,
		`{}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, graphJSON string) {
		if !json.Valid([]byte(graphJSON)) {
			return
		}
		var g events.Graph
		if err := json.Unmarshal([]byte(graphJSON), &g); err != nil {
			return
		}
		_ = domain.ValidateFlow(g) // must return, never panic
	})
}
