// SPDX-License-Identifier: AGPL-3.0-or-later

// Package layout assigns canvas coordinates to a flow graph's nodes. A graph
// stores no coordinates of its own, so an API client that authors a flow without
// positions would render as an unplaced pile in the builder. This is a Go port of
// the web builder's layoutLanes (web/src/lib/layout.ts): nodes are placed
// left-to-right by their longest-path depth from the roots and stacked into
// horizontal swimlanes by lane, deterministically. Positions are presentation
// only — the execution runtime never reads them.
package layout

import "github.com/e6qu/intraktible/decision-engine/events"

// Matches web/src/lib/layout.ts so a server-laid-out flow looks identical to one
// the builder placed.
const (
	columnW = 220
	rowH    = 110
	laneGap = 48
)

// Apply returns g with node positions filled by the swimlane auto-layout, but
// ONLY when no node already carries a position. The builder always sends explicit
// positions, so a UI-authored (or re-imported) flow is returned untouched and the
// user's custom layout is preserved; an API client that omits positions gets a
// sensible default. The layout is deterministic, so re-importing the same
// position-less document is a no-op (its etag is stable).
func Apply(g events.Graph) events.Graph {
	for i := range g.Nodes {
		if g.Nodes[i].Position != nil {
			return g // a custom layout was supplied — respect it
		}
	}
	pos := positions(g.Nodes, g.Edges)
	out := g
	out.Nodes = make([]events.Node, len(g.Nodes))
	copy(out.Nodes, g.Nodes)
	for i := range out.Nodes {
		if p, ok := pos[out.Nodes[i].ID]; ok {
			placed := p
			out.Nodes[i].Position = &placed
		}
	}
	return out
}

// positions computes each node's x/y: x from its longest-path depth (column), y by
// stacking within its lane. Lanes appear in first-seen order; a node with no lane
// falls into "Main".
func positions(nodes []events.Node, edges []events.Edge) map[string]events.NodePosition {
	depth := columnDepths(nodes, edges)

	var laneOrder []string
	byLane := map[string][]events.Node{}
	for _, n := range nodes {
		lane := n.Lane
		if lane == "" {
			lane = "Main"
		}
		if _, seen := byLane[lane]; !seen {
			laneOrder = append(laneOrder, lane)
		}
		byLane[lane] = append(byLane[lane], n)
	}

	pos := make(map[string]events.NodePosition, len(nodes))
	cursorY := 0
	for _, lane := range laneOrder {
		rowAtX := map[int]int{} // stack nodes sharing a depth column
		rows := 0
		for _, n := range byLane[lane] {
			x := depth[n.ID] * columnW
			row := rowAtX[x]
			rowAtX[x] = row + 1
			if row+1 > rows {
				rows = row + 1
			}
			pos[n.ID] = events.NodePosition{X: float64(x), Y: float64(cursorY + row*rowH)}
		}
		if rows < 1 {
			rows = 1
		}
		cursorY += rows*rowH + laneGap
	}
	return pos
}

// columnDepths returns each node's longest-path distance from a root via a Kahn
// traversal — the same column assignment the web layout uses. Edges referencing
// unknown nodes are ignored; the result is independent of processing order.
func columnDepths(nodes []events.Node, edges []events.Edge) map[string]int {
	known := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		known[n.ID] = true
	}
	adj := map[string][]string{}
	indeg := map[string]int{}
	for _, n := range nodes {
		indeg[n.ID] = 0
	}
	for _, e := range edges {
		if !known[e.From] || !known[e.To] {
			continue
		}
		adj[e.From] = append(adj[e.From], e.To)
		indeg[e.To]++
	}

	depth := map[string]int{}
	remaining := make(map[string]int, len(indeg))
	var queue []string
	// Seed roots in node declaration order for deterministic traversal.
	for _, n := range nodes {
		remaining[n.ID] = indeg[n.ID]
		if indeg[n.ID] == 0 {
			depth[n.ID] = 0
			queue = append(queue, n.ID)
		}
	}
	for i := 0; i < len(queue); i++ {
		id := queue[i]
		for _, next := range adj[id] {
			if depth[id]+1 > depth[next] {
				depth[next] = depth[id] + 1
			}
			remaining[next]--
			if remaining[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	return depth
}
