// SPDX-License-Identifier: AGPL-3.0-or-later

// Pure structural diff of two flow-version graphs, for the builder's version-diff
// view. Nodes are matched by id; edges by their (from, to, branch) key. A node is
// "changed" when its type, name, or config differ between the two versions.
import type { FlowGraph, GraphNode, GraphEdge } from './api';

export interface GraphDiff {
  nodesAdded: string[];
  nodesRemoved: string[];
  nodesChanged: string[];
  edgesAdded: string[];
  edgesRemoved: string[];
}

// canon renders a value as a key-sorted canonical JSON string so config equality
// is order-independent (built via entries, not dynamic indexing).
export function canon(v: unknown): string {
  if (v === null || typeof v !== 'object') return JSON.stringify(v) ?? 'null';
  if (Array.isArray(v)) return '[' + v.map(canon).join(',') + ']';
  const entries = Object.entries(v as Record<string, unknown>).sort(([a], [b]) =>
    a.localeCompare(b)
  );
  return '{' + entries.map(([k, val]) => JSON.stringify(k) + ':' + canon(val)).join(',') + '}';
}

function nodeSig(n: GraphNode): string {
  return canon({ type: n.type, name: n.name ?? '', config: n.config ?? null });
}

export function edgeKey(e: GraphEdge): string {
  return `${e.from} → ${e.to}${e.branch ? ` [${e.branch}]` : ''}`;
}

export function diffGraphs(a: FlowGraph, b: FlowGraph): GraphDiff {
  const aNodes = new Map(a.nodes.map((n) => [n.id, n]));
  const bNodes = new Map(b.nodes.map((n) => [n.id, n]));
  const nodesAdded: string[] = [];
  const nodesRemoved: string[] = [];
  const nodesChanged: string[] = [];
  for (const [id, bn] of bNodes) {
    const an = aNodes.get(id);
    if (!an) nodesAdded.push(id);
    else if (nodeSig(an) !== nodeSig(bn)) nodesChanged.push(id);
  }
  for (const id of aNodes.keys()) {
    if (!bNodes.has(id)) nodesRemoved.push(id);
  }

  const aEdges = new Set(a.edges.map(edgeKey));
  const bEdges = new Set(b.edges.map(edgeKey));
  const edgesAdded = [...bEdges].filter((k) => !aEdges.has(k));
  const edgesRemoved = [...aEdges].filter((k) => !bEdges.has(k));

  return {
    nodesAdded: nodesAdded.sort(),
    nodesRemoved: nodesRemoved.sort(),
    nodesChanged: nodesChanged.sort(),
    edgesAdded,
    edgesRemoved
  };
}

// diffIsEmpty reports whether two versions are structurally identical.
export function diffIsEmpty(d: GraphDiff): boolean {
  return (
    d.nodesAdded.length === 0 &&
    d.nodesRemoved.length === 0 &&
    d.nodesChanged.length === 0 &&
    d.edgesAdded.length === 0 &&
    d.edgesRemoved.length === 0
  );
}
