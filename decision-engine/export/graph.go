// SPDX-License-Identifier: AGPL-3.0-or-later

package export

import (
	"fmt"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// soundEdges returns the edges whose endpoints are BOTH declared nodes. An edge to
// an undeclared node has no shape to attach to — it yields invalid BPMN (a
// sequenceFlow/waypoint referencing a missing element) and a phantom implicit node
// in Mermaid/DOT. A graph from a published flow has none (the domain validator
// rejects dangling edges); filtering here keeps arbitrary or partially-edited graphs
// from rendering a broken diagram, in one place shared by every exporter.
func soundEdges(g events.Graph) []events.Edge {
	known := make(map[string]bool, len(g.Nodes))
	for _, n := range g.Nodes {
		known[n.ID] = true
	}
	out := make([]events.Edge, 0, len(g.Edges))
	for _, e := range g.Edges {
		if known[e.From] && known[e.To] {
			out = append(out, e)
		}
	}
	return out
}

// assignIDs maps each node id (and each edge endpoint, through the SAME uniqueness
// set) to a unique identifier produced by coerce, suffixing collisions (_2, _3, …).
// coerce alone can map two distinct ids to one string (e.g. "a.b" and "a/b" → "a_b"),
// which would merge those nodes and cross-wire every edge referencing either in the
// rendered diagram. Shared by the BPMN and Mermaid exporters (DOT quotes raw ids, so
// it has no coercion-collision class).
func assignIDs(nodes []events.Node, edges []events.Edge, coerce func(string) string) map[string]string {
	used := map[string]bool{}
	ids := make(map[string]string, len(nodes))
	assign := func(raw string) {
		if _, done := ids[raw]; done {
			return
		}
		base := coerce(raw)
		cand := base
		for i := 2; used[cand]; i++ {
			cand = fmt.Sprintf("%s_%d", base, i)
		}
		used[cand] = true
		ids[raw] = cand
	}
	for _, n := range nodes {
		assign(n.ID)
	}
	for _, e := range edges {
		assign(e.From)
		assign(e.To)
	}
	return ids
}
