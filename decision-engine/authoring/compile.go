// SPDX-License-Identifier: AGPL-3.0-or-later

package authoring

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
)

const (
	maxComponentDepth = 64
	maxExpandedNodes  = 10000
)

// ComponentVersion is the immutable content the compiler resolves.
type ComponentVersion struct {
	ComponentID         string
	Version             int
	Etag                string
	SourceGraph         events.Graph
	InputSchema         json.RawMessage
	OutputSchema        json.RawMessage
	Event               eventlog.Envelope
	MutationKeyHash     string
	MutationRequestHash string
}

// ComponentResolver resolves an exact immutable version.
type ComponentResolver interface {
	ResolveComponent(componentID string, version int) (ComponentVersion, bool)
}

// Compile expands every subflow node recursively into ordinary runtime nodes.
// It returns exact transitive dependency pins in deterministic order.
func Compile(graph events.Graph, resolver ComponentResolver) (events.Graph, []events.FlowDependency, error) {
	if err := validateAuthoringGraph(graph); err != nil {
		return events.Graph{}, nil, err
	}
	deps := make(map[string]events.FlowDependency)
	compiled, err := compileGraph(graph, resolver, nil, deps)
	if err != nil {
		return events.Graph{}, nil, err
	}
	// Review validation and publication must share the same dry compiler.
	// Structural validation alone would let malformed node configuration receive
	// a passing changeset check and then fail only at the publication boundary.
	if err := domain.ValidateFlow(compiled); err != nil {
		return events.Graph{}, nil, fmt.Errorf("authoring: compiled graph: %w", err)
	}
	out := make([]events.FlowDependency, 0, len(deps))
	for _, dep := range deps {
		out = append(out, dep)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ComponentID == out[j].ComponentID {
			return out[i].Version < out[j].Version
		}
		return out[i].ComponentID < out[j].ComponentID
	})
	return compiled, out, nil
}

func validateAuthoringGraph(graph events.Graph) error {
	if err := validateDraftGraph(graph); err != nil {
		return err
	}
	if err := domain.ValidateAuthoringGraph(graph); err != nil {
		return err
	}
	for _, node := range graph.Nodes {
		if node.Type != events.NodeSubflow {
			continue
		}
		var cfg SubflowConfig
		if err := json.Unmarshal(node.Config, &cfg); err != nil {
			return fmt.Errorf("authoring: subflow node %q config: %w", node.ID, err)
		}
		hasID := strings.TrimSpace(cfg.ComponentID) != ""
		hasSlug := strings.TrimSpace(cfg.ComponentSlug) != ""
		if hasID == hasSlug || cfg.Version < 1 {
			return fmt.Errorf(
				"authoring: subflow node %q requires exactly one of component_id or "+
					"component_slug and an exact positive version",
				node.ID,
			)
		}
	}
	return nil
}

func compileGraph(
	graph events.Graph,
	resolver ComponentResolver,
	stack []string,
	deps map[string]events.FlowDependency,
) (events.Graph, error) {
	if len(stack) > maxComponentDepth {
		return events.Graph{}, fmt.Errorf(
			"authoring: reusable component nesting exceeds %d levels",
			maxComponentDepth,
		)
	}
	result := cloneGraph(graph)
	if len(result.Nodes) > maxExpandedNodes {
		return events.Graph{}, fmt.Errorf(
			"authoring: expanded graph exceeds %d nodes",
			maxExpandedNodes,
		)
	}
	for {
		index := -1
		for i := range result.Nodes {
			if result.Nodes[i].Type == events.NodeSubflow {
				index = i
				break
			}
		}
		if index < 0 {
			return result, nil
		}
		node := result.Nodes[index]
		var cfg SubflowConfig
		if err := json.Unmarshal(node.Config, &cfg); err != nil {
			return events.Graph{}, fmt.Errorf("authoring: subflow node %q config: %w", node.ID, err)
		}
		if cfg.ComponentID == "" {
			return events.Graph{}, fmt.Errorf(
				"authoring: unresolved canonical component slug %q on node %q",
				cfg.ComponentSlug, node.ID,
			)
		}
		key := fmt.Sprintf("%s@%d", cfg.ComponentID, cfg.Version)
		for _, active := range stack {
			if active == key {
				return events.Graph{}, fmt.Errorf(
					"authoring: reusable component cycle %s",
					strings.Join(append(stack, key), " -> "),
				)
			}
		}
		component, ok := resolver.ResolveComponent(cfg.ComponentID, cfg.Version)
		if !ok {
			return events.Graph{}, fmt.Errorf("authoring: unknown component version %s", key)
		}
		deps[key] = events.FlowDependency{
			ComponentID: component.ComponentID,
			Version:     component.Version,
			Etag:        component.Etag,
		}
		expanded, err := compileGraph(
			component.SourceGraph,
			resolver,
			append(append([]string(nil), stack...), key),
			deps,
		)
		if err != nil {
			return events.Graph{}, err
		}
		result, err = spliceSubflow(result, node.ID, expanded)
		if err != nil {
			return events.Graph{}, err
		}
		if len(result.Nodes) > maxExpandedNodes {
			return events.Graph{}, fmt.Errorf(
				"authoring: expanded graph exceeds %d nodes",
				maxExpandedNodes,
			)
		}
	}
}

func spliceSubflow(outer events.Graph, nodeID string, inner events.Graph) (events.Graph, error) {
	var inputID, outputID string
	for _, node := range inner.Nodes {
		switch node.Type {
		case events.NodeInput:
			if inputID != "" {
				return events.Graph{}, fmt.Errorf("authoring: component for node %q has multiple inputs", nodeID)
			}
			inputID = node.ID
		case events.NodeOutput:
			if outputID != "" {
				return events.Graph{}, fmt.Errorf("authoring: component for node %q has multiple outputs", nodeID)
			}
			outputID = node.ID
		}
	}
	if inputID == "" || outputID == "" {
		return events.Graph{}, fmt.Errorf("authoring: component for node %q needs one input and output", nodeID)
	}

	incoming, outgoing := make([]events.Edge, 0), make([]events.Edge, 0)
	keptEdges := make([]events.Edge, 0, len(outer.Edges)+len(inner.Edges))
	for _, edge := range outer.Edges {
		switch {
		case edge.To == nodeID:
			incoming = append(incoming, edge)
		case edge.From == nodeID:
			outgoing = append(outgoing, edge)
		default:
			keptEdges = append(keptEdges, edge)
		}
	}

	prefix := nodeID + "::"
	first, last := make([]events.Edge, 0), make([]events.Edge, 0)
	for _, edge := range inner.Edges {
		switch {
		case edge.From == inputID && edge.To == outputID:
			first = append(first, edge)
			last = append(last, edge)
		case edge.From == inputID:
			if edge.Branch != "" {
				return events.Graph{}, fmt.Errorf("authoring: component input edge cannot be branched")
			}
			first = append(first, edge)
		case edge.To == outputID:
			if edge.Branch != "" {
				return events.Graph{}, fmt.Errorf("authoring: component output edge cannot be branched")
			}
			last = append(last, edge)
		default:
			keptEdges = append(keptEdges, events.Edge{
				From: prefix + edge.From, To: prefix + edge.To, Branch: edge.Branch,
			})
		}
	}

	direct := false
	for _, edge := range inner.Edges {
		if edge.From == inputID && edge.To == outputID {
			direct = true
			break
		}
	}
	if direct {
		for _, in := range incoming {
			for _, out := range outgoing {
				branch, err := mergeBranch(in.Branch, out.Branch)
				if err != nil {
					return events.Graph{}, fmt.Errorf("authoring: subflow node %q: %w", nodeID, err)
				}
				keptEdges = append(keptEdges, events.Edge{From: in.From, To: out.To, Branch: branch})
			}
		}
	} else {
		for _, in := range incoming {
			for _, start := range first {
				if start.To == outputID {
					continue
				}
				keptEdges = append(keptEdges, events.Edge{
					From: in.From, To: prefix + start.To, Branch: in.Branch,
				})
			}
		}
		for _, end := range last {
			if end.From == inputID {
				continue
			}
			for _, out := range outgoing {
				keptEdges = append(keptEdges, events.Edge{
					From: prefix + end.From, To: out.To, Branch: out.Branch,
				})
			}
		}
	}

	nodes := make([]events.Node, 0, len(outer.Nodes)+len(inner.Nodes)-3)
	for _, node := range outer.Nodes {
		if node.ID != nodeID {
			nodes = append(nodes, node)
		}
	}
	for _, node := range inner.Nodes {
		if node.ID == inputID || node.ID == outputID {
			continue
		}
		node.ID = prefix + node.ID
		nodes = append(nodes, node)
	}
	return events.Graph{Nodes: nodes, Edges: keptEdges}, nil
}

func mergeBranch(a, b string) (string, error) {
	if a != "" && b != "" && a != b {
		return "", fmt.Errorf("conflicting boundary branches %q and %q", a, b)
	}
	if a != "" {
		return a, nil
	}
	return b, nil
}

func cloneGraph(graph events.Graph) events.Graph {
	out := graph
	out.Nodes = append([]events.Node(nil), graph.Nodes...)
	out.Edges = append([]events.Edge(nil), graph.Edges...)
	for i := range out.Nodes {
		out.Nodes[i].Config = append(json.RawMessage(nil), graph.Nodes[i].Config...)
	}
	return out
}
