// SPDX-License-Identifier: AGPL-3.0-or-later

package agents

import (
	"testing"

	"github.com/e6qu/intraktible/platform/ai"
)

// A turn's tool calls must come out with non-empty, unique IDs — the correlation key
// between the assistant's tool_calls and the tool-result messages. A model that
// returns empty or duplicate IDs must not be able to break that pairing.
func TestNormalizeToolCallIDs(t *testing.T) {
	in := []ai.ToolCall{
		{ID: "", Name: "a"},
		{ID: "", Name: "b"},
		{ID: "dup", Name: "c"},
		{ID: "dup", Name: "d"},
		{ID: "keep", Name: "e"},
	}
	out := normalizeToolCallIDs(in)
	if len(out) != len(in) {
		t.Fatalf("length changed: %d != %d", len(out), len(in))
	}
	seen := map[string]bool{}
	for i, c := range out {
		if c.ID == "" {
			t.Fatalf("call %d (%s) still has an empty ID", i, c.Name)
		}
		if seen[c.ID] {
			t.Fatalf("duplicate ID %q after normalization", c.ID)
		}
		seen[c.ID] = true
		if c.Name != in[i].Name {
			t.Fatalf("call %d name reordered: %q != %q", i, c.Name, in[i].Name)
		}
	}
	// A non-empty, unique ID is preserved verbatim.
	if out[4].ID != "keep" {
		t.Fatalf("a unique non-empty ID must be preserved, got %q", out[4].ID)
	}
}
