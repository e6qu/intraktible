// SPDX-License-Identifier: AGPL-3.0-or-later

// Package domain is the Decision Engine's functional core: pure flow-model
// validation and content hashing, with no I/O. It must stay deterministic so
// that validation and etags reproduce exactly on replay.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/e6qu/intraktible/decision-engine/events"
)

var nodeTypes = map[events.NodeType]bool{
	events.NodeInput:         true,
	events.NodeRule:          true,
	events.NodeSplit:         true,
	events.NodeAssignment:    true,
	events.NodeScorecard:     true,
	events.NodeDecisionTable: true,
	events.NodeMatrix2D:      true,
	events.NodeCode:          true,
	events.NodeAI:            true,
	events.NodeConnect:       true,
	events.NodeManualReview:  true,
	events.NodeReason:        true,
	events.NodeOutput:        true,
}

// ValidateGraph fails loudly on a structurally invalid flow graph: it requires
// unique non-empty node IDs of known types, exactly one Input and at least one
// Output node, edges that reference existing distinct nodes, and acyclicity.
// Per-node Config is not inspected here — each node engine validates its own
// config at decide time.
func ValidateGraph(g events.Graph) error {
	if len(g.Nodes) == 0 {
		return errors.New("decision-engine: graph has no nodes")
	}
	types := make(map[string]events.NodeType, len(g.Nodes))
	var inputs, outputs int
	for _, n := range g.Nodes {
		if strings.TrimSpace(n.ID) == "" {
			return errors.New("decision-engine: node with empty id")
		}
		if _, dup := types[n.ID]; dup {
			return fmt.Errorf("decision-engine: duplicate node id %q", n.ID)
		}
		if !nodeTypes[n.Type] {
			return fmt.Errorf("decision-engine: node %q has unknown type %q", n.ID, n.Type)
		}
		types[n.ID] = n.Type
		switch n.Type {
		case events.NodeInput:
			inputs++
		case events.NodeOutput:
			outputs++
		}
	}
	if inputs != 1 {
		return fmt.Errorf("decision-engine: graph needs exactly one input node, got %d", inputs)
	}
	if outputs < 1 {
		return errors.New("decision-engine: graph needs at least one output node")
	}
	return validateEdgesAcyclic(g, types)
}

// validateEdgesAcyclic checks edge endpoints and rejects cycles via Kahn's
// algorithm. Nodes are processed in declaration order so the traversal is
// deterministic (a prerequisite for the execution runtime to come).
func validateEdgesAcyclic(g events.Graph, types map[string]events.NodeType) error {
	indeg := make(map[string]int, len(g.Nodes))
	for id := range types {
		indeg[id] = 0
	}
	adj := make(map[string][]string, len(g.Nodes))
	for _, e := range g.Edges {
		if _, ok := types[e.From]; !ok {
			return fmt.Errorf("decision-engine: edge from unknown node %q", e.From)
		}
		if _, ok := types[e.To]; !ok {
			return fmt.Errorf("decision-engine: edge to unknown node %q", e.To)
		}
		if e.From == e.To {
			return fmt.Errorf("decision-engine: self-loop on node %q", e.From)
		}
		adj[e.From] = append(adj[e.From], e.To)
		indeg[e.To]++
	}
	queue := make([]string, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		if indeg[n.ID] == 0 {
			queue = append(queue, n.ID)
		}
	}
	var visited int
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, to := range adj[id] {
			indeg[to]--
			if indeg[to] == 0 {
				queue = append(queue, to)
			}
		}
	}
	if visited != len(g.Nodes) {
		return errors.New("decision-engine: graph has a cycle")
	}
	return nil
}

// Etag is the content hash of a flow version's graph and input schema. Identical
// content yields an identical etag, so a no-op republish is detectable and the
// value is stable across replay.
func Etag(g events.Graph, inputSchema json.RawMessage) (string, error) {
	b, err := json.Marshal(struct {
		Graph       events.Graph    `json:"graph"`
		InputSchema json.RawMessage `json:"input_schema,omitempty"`
	}{Graph: g, InputSchema: inputSchema})
	if err != nil {
		return "", fmt.Errorf("decision-engine: hash version: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
