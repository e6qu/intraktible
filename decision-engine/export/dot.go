// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"fmt"
	"strings"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// DOT renders the graph as Graphviz DOT (a `digraph`), mapping each node type to a
// shape and labelling conditional (branch) edges. Pipe it to `dot -Tsvg` /
// `dot -Tpng`, or paste into any Graphviz viewer. Pure and deterministic.
func DOT(g events.Graph) string {
	var b strings.Builder
	b.WriteString("digraph flow {\n")
	b.WriteString("  rankdir=TB;\n")
	b.WriteString(`  node [fontname="Helvetica,Arial,sans-serif"];` + "\n")
	b.WriteString(`  edge [fontname="Helvetica,Arial,sans-serif"];` + "\n")
	for _, n := range g.Nodes {
		fmt.Fprintf(&b, "  %s [label=%s, shape=%s];\n", dotQuote(n.ID), dotQuote(nodeLabel(n)), dotShape(n.Type))
	}
	// Skip edges to undeclared nodes so DOT doesn't synthesize a phantom, unlabelled
	// node (consistent with the BPMN/Mermaid exporters).
	for _, e := range soundEdges(g) {
		if e.Branch != "" {
			fmt.Fprintf(&b, "  %s -> %s [label=%s];\n", dotQuote(e.From), dotQuote(e.To), dotQuote(e.Branch))
		} else {
			fmt.Fprintf(&b, "  %s -> %s;\n", dotQuote(e.From), dotQuote(e.To))
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// RunDOT renders one decision run as a Graphviz `digraph`: a start node, the
// nodes visited in execution order (left to right), and a terminal status node —
// the path the decision actually took.
func RunDOT(flow string, steps []RunStep, status string) string {
	const startID, endID = "__start", "__end"
	if status == "" {
		status = "done"
	}
	var b strings.Builder
	b.WriteString("digraph run {\n")
	b.WriteString("  rankdir=LR;\n")
	b.WriteString(`  node [fontname="Helvetica,Arial,sans-serif"];` + "\n")
	fmt.Fprintf(&b, "  %s [label=%s, shape=circle];\n", dotQuote(startID), dotQuote("decide: "+flow))
	for _, s := range steps {
		fmt.Fprintf(&b, "  %s [label=%s, shape=box];\n", dotQuote(s.NodeID), dotQuote(fmt.Sprintf("%s (%s)", s.NodeID, s.Type)))
	}
	fmt.Fprintf(&b, "  %s [label=%s, shape=doublecircle];\n", dotQuote(endID), dotQuote(status))
	prev := startID
	for _, s := range steps {
		fmt.Fprintf(&b, "  %s -> %s;\n", dotQuote(prev), dotQuote(s.NodeID))
		prev = s.NodeID
	}
	fmt.Fprintf(&b, "  %s -> %s;\n", dotQuote(prev), dotQuote(endID))
	b.WriteString("}\n")
	return b.String()
}

// dotShape maps a node type to a Graphviz shape (mirrors the Mermaid shape map).
func dotShape(t events.NodeType) string {
	switch t {
	case events.NodeInput, events.NodeOutput:
		return "ellipse" // start/end
	case events.NodeSplit:
		return "diamond" // decision
	case events.NodeRule, events.NodeDecisionTable, events.NodeScorecard, events.NodeMatrix2D:
		return "box3d" // rule-like
	case events.NodeAI, events.NodeConnect, events.NodePredict:
		return "cylinder" // external/data
	case events.NodeManualReview:
		return "hexagon" // human gate
	default:
		return "box" // assignment, code, reason, …
	}
}

// dotQuote wraps text as a quoted DOT string, escaping quotes/backslashes and
// flattening newlines so the output is always a single valid token.
func dotQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ", "\r", " ")
	return `"` + r.Replace(s) + `"`
}
