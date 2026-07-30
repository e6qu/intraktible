// SPDX-License-Identifier: AGPL-3.0-or-later

package authoring

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// Difference is one execution-relevant semantic change between two authoring
// graphs. Layout-only position changes are intentionally excluded.
type Difference struct {
	Kind   string `json:"kind"`
	Object string `json:"object"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

// SemanticDiff compares nodes and edges by stable identity while ignoring
// presentation position. Config JSON is normalized structurally.
func SemanticDiff(before, after events.Graph) ([]Difference, error) {
	left, err := semanticNodes(before)
	if err != nil {
		return nil, err
	}
	right, err := semanticNodes(after)
	if err != nil {
		return nil, err
	}
	diffs := make([]Difference, 0)
	keys := unionKeys(left, right)
	for _, key := range keys {
		a, aOK := left[key]
		b, bOK := right[key]
		switch {
		case !aOK:
			diffs = append(diffs, Difference{Kind: "node_added", Object: key, After: b})
		case !bOK:
			diffs = append(diffs, Difference{Kind: "node_removed", Object: key, Before: a})
		case !semanticEqual(a, b):
			diffs = append(diffs, Difference{Kind: "node_changed", Object: key, Before: a, After: b})
		}
	}
	leftEdges := semanticEdges(before)
	rightEdges := semanticEdges(after)
	for _, key := range unionKeys(leftEdges, rightEdges) {
		a, aOK := leftEdges[key]
		b, bOK := rightEdges[key]
		switch {
		case !aOK:
			diffs = append(diffs, Difference{Kind: "edge_added", Object: key, After: b})
		case !bOK:
			diffs = append(diffs, Difference{Kind: "edge_removed", Object: key, Before: a})
		}
	}
	return diffs, nil
}

// SemanticChangeSetDiff extends graph differences with schema and exact reusable
// dependency pins. These values affect validation/runtime lineage even when the
// expanded graph happens to look identical.
func SemanticChangeSetDiff(
	beforeGraph events.Graph,
	beforeSchema json.RawMessage,
	beforeDependencies []events.FlowDependency,
	afterGraph events.Graph,
	afterSchema json.RawMessage,
	afterDependencies []events.FlowDependency,
) ([]Difference, error) {
	differences, err := SemanticDiff(beforeGraph, afterGraph)
	if err != nil {
		return nil, err
	}
	leftSchema, err := canonicalJSON(beforeSchema)
	if err != nil {
		return nil, fmt.Errorf("authoring: base input schema: %w", err)
	}
	rightSchema, err := canonicalJSON(afterSchema)
	if err != nil {
		return nil, fmt.Errorf("authoring: proposed input schema: %w", err)
	}
	if !bytes.Equal(leftSchema, rightSchema) {
		differences = append(differences, Difference{
			Kind: "input_schema_changed", Object: "input_schema",
			Before: rawJSONValue(leftSchema), After: rawJSONValue(rightSchema),
		})
	}
	leftDependencies := dependencyMap(beforeDependencies)
	rightDependencies := dependencyMap(afterDependencies)
	for _, componentID := range unionKeys(leftDependencies, rightDependencies) {
		left, leftOK := leftDependencies[componentID]
		right, rightOK := rightDependencies[componentID]
		switch {
		case !leftOK:
			differences = append(differences, Difference{
				Kind: "dependency_added", Object: componentID, After: right,
			})
		case !rightOK:
			differences = append(differences, Difference{
				Kind: "dependency_removed", Object: componentID, Before: left,
			})
		case !semanticEqual(left, right):
			differences = append(differences, Difference{
				Kind: "dependency_changed", Object: componentID,
				Before: left, After: right,
			})
		}
	}
	return differences, nil
}

func dependencyMap(
	dependencies []events.FlowDependency,
) map[string]events.FlowDependency {
	out := make(map[string]events.FlowDependency, len(dependencies))
	for _, dependency := range dependencies {
		out[dependency.ComponentID] = dependency
	}
	return out
}

func rawJSONValue(canonical []byte) any {
	if len(canonical) == 0 {
		return nil
	}
	return json.RawMessage(canonical)
}

func semanticNodes(graph events.Graph) (map[string]any, error) {
	out := make(map[string]any, len(graph.Nodes))
	for _, node := range graph.Nodes {
		var config any
		if len(node.Config) > 0 {
			if err := json.Unmarshal(node.Config, &config); err != nil {
				return nil, fmt.Errorf("authoring: node %q config: %w", node.ID, err)
			}
		}
		out[node.ID] = struct {
			Type   events.NodeType `json:"type"`
			Name   string          `json:"name,omitempty"`
			Config any             `json:"config,omitempty"`
			Lane   string          `json:"lane,omitempty"`
		}{Type: node.Type, Name: node.Name, Config: config, Lane: node.Lane}
	}
	return out, nil
}

func semanticEdges(graph events.Graph) map[string]any {
	out := make(map[string]any, len(graph.Edges))
	for _, edge := range graph.Edges {
		key := edge.From + "\x00" + edge.To + "\x00" + edge.Branch
		out[key] = edge
	}
	return out
}

func unionKeys[T any](a, b map[string]T) []string {
	set := make(map[string]bool, len(a)+len(b))
	for key := range a {
		set[key] = true
	}
	for key := range b {
		set[key] = true
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func semanticEqual(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}
