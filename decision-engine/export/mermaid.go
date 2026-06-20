// SPDX-License-Identifier: AGPL-3.0-or-later

// Package export renders a flow graph (and a decision run) to portable diagram
// formats — Mermaid (flowchart, state, and sequence diagrams) and BPMN 2.0 XML
// with diagram-interchange layout. It is a pure, dependency-light transform: no
// I/O, deterministic output for a given input.
package export

import (
	"fmt"
	"strings"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// MermaidFlowchart renders the graph as a Mermaid `flowchart TD`, mapping each
// node type to a distinct shape and labelling conditional (branch) edges.
func MermaidFlowchart(g events.Graph) string {
	ids := assignIDs(g.Nodes, g.Edges, mermaidID)
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	for _, n := range g.Nodes {
		open, closeTok := flowchartShape(n.Type)
		fmt.Fprintf(&b, "  %s%s%s%s\n", ids[n.ID], open, mermaidLabel(nodeLabel(n)), closeTok)
	}
	for _, e := range soundEdges(g) {
		if e.Branch != "" {
			fmt.Fprintf(&b, "  %s -->|%s| %s\n", ids[e.From], mermaidInline(e.Branch), ids[e.To])
		} else {
			fmt.Fprintf(&b, "  %s --> %s\n", ids[e.From], ids[e.To])
		}
	}
	return b.String()
}

// MermaidState renders the graph as a Mermaid `stateDiagram-v2`: each node is a
// state (input from the start pseudo-state, output to the end pseudo-state) and
// each edge a transition, with branch labels.
func MermaidState(g events.Graph) string {
	ids := assignIDs(g.Nodes, g.Edges, mermaidID)
	var b strings.Builder
	b.WriteString("stateDiagram-v2\n")
	for _, n := range g.Nodes {
		fmt.Fprintf(&b, "  state %s as %s\n", mermaidLabel(nodeLabel(n)), ids[n.ID])
	}
	for _, n := range g.Nodes {
		if n.Type == events.NodeInput {
			fmt.Fprintf(&b, "  [*] --> %s\n", ids[n.ID])
		}
	}
	for _, e := range soundEdges(g) {
		if e.Branch != "" {
			fmt.Fprintf(&b, "  %s --> %s : %s\n", ids[e.From], ids[e.To], mermaidInline(e.Branch))
		} else {
			fmt.Fprintf(&b, "  %s --> %s\n", ids[e.From], ids[e.To])
		}
	}
	for _, n := range g.Nodes {
		if n.Type == events.NodeOutput {
			fmt.Fprintf(&b, "  %s --> [*]\n", ids[n.ID])
		}
	}
	return b.String()
}

// RunStep is one node's evaluation within a decision run (the caller maps a
// recorded decision's node trace to these).
type RunStep struct {
	NodeID string
	Type   string
}

// MermaidSequence renders one decision run as a Mermaid `sequenceDiagram`: the
// client asks the flow to decide, the flow visits each node in execution order,
// and replies with the final status.
func MermaidSequence(flow string, steps []RunStep, status string) string {
	var b strings.Builder
	b.WriteString("sequenceDiagram\n  autonumber\n")
	b.WriteString("  participant C as Client\n")
	fmt.Fprintf(&b, "  participant E as %s\n", mermaidInline(flow))
	b.WriteString("  C->>E: decide\n")
	for _, s := range steps {
		fmt.Fprintf(&b, "  Note over E: %s (%s)\n", mermaidInline(s.NodeID), mermaidInline(s.Type))
	}
	if status == "" {
		status = "done"
	}
	fmt.Fprintf(&b, "  E-->>C: %s\n", mermaidInline(status))
	return b.String()
}

// flowchartShape returns the Mermaid bracket pair for a node type's shape.
func flowchartShape(t events.NodeType) (open, closeTok string) {
	switch t {
	case events.NodeInput, events.NodeOutput:
		return "([", "])" // stadium (start/end)
	case events.NodeSplit:
		return "{", "}" // decision diamond
	case events.NodeRule, events.NodeDecisionTable, events.NodeScorecard, events.NodeMatrix2D:
		return "[[", "]]" // subroutine (rule-like)
	case events.NodeAI, events.NodeConnect:
		return "[(", ")]" // cylinder (external/data)
	case events.NodeManualReview:
		return "{{", "}}" // hexagon (human gate)
	default:
		return "[", "]" // rectangle (assignment, code)
	}
}

// nodeLabel is the human label for a node: its name (or id) plus its type.
func nodeLabel(n events.Node) string {
	name := n.Name
	if strings.TrimSpace(name) == "" {
		name = n.ID
	}
	return fmt.Sprintf("%s (%s)", name, n.Type)
}

// mermaidID sanitizes a node id to a Mermaid-safe identifier.
func mermaidID(id string) string {
	var b strings.Builder
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if i == 0 {
				b.WriteByte('n')
			}
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "n"
	}
	return b.String()
}

// mermaidLabel wraps text as a quoted Mermaid label, neutralizing characters that
// would break the quoting.
func mermaidLabel(text string) string {
	return `"` + mermaidInline(text) + `"`
}

// mermaidInline neutralizes characters that break inline Mermaid text.
func mermaidInline(text string) string {
	r := strings.NewReplacer(
		`"`, "'",
		"\n", " ",
		"\r", " ",
		"|", "/",
		"[", "(",
		"]", ")",
		"{", "(",
		"}", ")",
		";", ",",
	)
	return r.Replace(text)
}
