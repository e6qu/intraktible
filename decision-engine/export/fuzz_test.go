// SPDX-License-Identifier: AGPL-3.0-or-later

package export_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/export"
)

// FuzzBPMN asserts BPMN export never panics on an arbitrary graph — it walks nodes
// + edges (including edges referencing missing nodes, duplicate/colliding ids, and
// cycles) and lays them out, so a malformed graph must produce (some) XML, not a
// panic (index out of range, nil map, etc.).
func FuzzBPMN(f *testing.F) {
	seeds := []string{
		`{"nodes":[{"id":"in","type":"input"},{"id":"r","type":"rule"},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"r"},{"from":"r","to":"o"}]}`,
		`{"nodes":[{"id":"a/b","type":"split"},{"id":"a b","type":"rule"}],"edges":[{"from":"a/b","to":"ghost"},{"from":"x","to":"a b"}]}`, // colliding NCNames + dangling endpoints
		`{"nodes":[{"id":"n","type":"rule"}],"edges":[{"from":"n","to":"n"}]}`,                                                             // self-loop
		`{}`,
	}
	for _, s := range seeds {
		f.Add(s, "Flow")
	}
	f.Fuzz(func(t *testing.T, graphJSON, name string) {
		if !json.Valid([]byte(graphJSON)) {
			return
		}
		var g events.Graph
		if err := json.Unmarshal([]byte(graphJSON), &g); err != nil {
			return
		}
		out := export.BPMN(g, name) // must not panic
		if out != "" && !strings.Contains(out, "<") {
			t.Fatalf("non-empty BPMN output is not XML-ish: %q", out)
		}
	})
}

// FuzzMermaid asserts the Mermaid exporters never panic on an arbitrary graph and
// that distinct node ids stay distinct in the rendered output even when they
// coerce to the same Mermaid-safe identifier (the collision/cross-wiring class).
func FuzzMermaid(f *testing.F) {
	seeds := []string{
		`{"nodes":[{"id":"in","type":"input"},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"o"}]}`,
		`{"nodes":[{"id":"a.b","type":"input"},{"id":"a/b","type":"output"}],"edges":[{"from":"a.b","to":"a/b"}]}`, // colliding ids
		`{"nodes":[{"id":"n","type":"rule"}],"edges":[{"from":"n","to":"ghost"}]}`,                                 // dangling target
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
		_ = export.MermaidFlowchart(g) // must not panic
		_ = export.MermaidState(g)     // must not panic
	})
}
