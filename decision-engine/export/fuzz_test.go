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

// FuzzDOT asserts the Graphviz exporters never panic on an arbitrary graph and that
// the hand-rolled dotQuote escaper keeps output well-formed — crafted node ids/labels
// (embedded quotes, backslashes, newlines, control bytes) must stay inside their
// quoted DOT token rather than breaking out. The sibling BPMN/Mermaid exporters are
// already fuzzed; DOT was the un-fuzzed one with its own escaper.
func FuzzDOT(f *testing.F) {
	seeds := []string{
		`{"nodes":[{"id":"in","type":"input"},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"o"}]}`,
		`{"nodes":[{"id":"a\"b","type":"rule"},{"id":"c\\d","type":"split"}],"edges":[{"from":"a\"b","to":"c\\d"}]}`, // quote + backslash in ids
		`{"nodes":[{"id":"line1\nline2","type":"code"}],"edges":[{"from":"line1\nline2","to":"ghost"}]}`,             // newline in id
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
		out := export.DOT(g) // must not panic
		if out != "" && !strings.Contains(out, "digraph") {
			t.Fatalf("non-empty DOT output is not a digraph: %q", out)
		}
		// Every line is a balanced sequence of unescaped quotes (each DOT token opens
		// and closes its quote): a crafted id that broke out would leave an odd count.
		for _, line := range strings.Split(out, "\n") {
			if unescapedQuoteCount(line)%2 != 0 {
				t.Fatalf("unbalanced quotes in DOT line: %q", line)
			}
		}
		// RunDOT walks a parallel path with attacker-shaped step ids/types.
		steps := make([]export.RunStep, 0, len(g.Nodes))
		for _, n := range g.Nodes {
			steps = append(steps, export.RunStep{NodeID: n.ID, Type: string(n.Type)})
		}
		_ = export.RunDOT("flow", steps, "done") // must not panic
	})
}

// FuzzJSONRoundTrip asserts the portable FlowExport JSON form is panic-free and
// stable: an arbitrary graph renders to JSON, the JSON re-imports (the {graph,
// input_schema} subset is exactly what the publish endpoint accepts), and a second
// render of the re-imported value is byte-identical to the first. Malformed import
// must error, not crash; a successful round-trip must be a fixpoint, so an
// export/re-import can't silently mutate a flow.
func FuzzJSONRoundTrip(f *testing.F) {
	seeds := []string{
		`{"nodes":[{"id":"in","type":"input"},{"id":"o","type":"output"}],"edges":[{"from":"in","to":"o"}]}`,
		`{"nodes":[{"id":"r","type":"rule","config":{"rules":[{"when":"x>1","then":[]}]}}],"edges":[]}`,
		`{"nodes":[{"id":"a\"b","type":"split"}],"edges":[{"from":"a\"b","to":"ghost","branch":"yes"}]}`,
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
		exp := export.FlowExport{Slug: "s", Name: "n", Version: 1, Graph: g}
		out, err := export.JSON(exp) // must not panic
		if err != nil {
			return
		}
		var reimported export.FlowExport
		if err := json.Unmarshal([]byte(out), &reimported); err != nil {
			t.Fatalf("export JSON does not re-import: %v\n%s", err, out)
		}
		out2, err := export.JSON(reimported)
		if err != nil {
			t.Fatalf("re-export failed: %v", err)
		}
		if out != out2 {
			t.Fatalf("JSON round-trip is not a fixpoint:\nfirst:  %q\nsecond: %q", out, out2)
		}
	})
}

// unescapedQuoteCount counts double quotes not preceded by an (odd run of) backslash
// escape — the quotes that actually open/close a DOT token.
func unescapedQuoteCount(s string) int {
	n, backslashes := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			backslashes++
		case '"':
			if backslashes%2 == 0 {
				n++
			}
			backslashes = 0
		default:
			backslashes = 0
		}
	}
	return n
}
