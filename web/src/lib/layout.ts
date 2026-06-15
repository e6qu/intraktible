// SPDX-License-Identifier: AGPL-3.0-or-later

// A flow graph stores no coordinates, so the builder lays nodes out by their
// longest-path depth from the roots: left-to-right by stage, stacked per stage.
// Dynamic-keyed collections use Map (not plain objects) so indexing is not a
// property-injection sink.

export interface XY {
  x: number;
  y: number;
}

interface NodeRef {
  id: string;
}

interface EdgeRef {
  from: string;
  to: string;
}

const COLUMN = 220;
const ROW = 110;

export function layout(nodes: NodeRef[], edges: EdgeRef[]): Map<string, XY> {
  const adjacency = new Map<string, string[]>();
  const indegree = new Map<string, number>();
  for (const n of nodes) {
    adjacency.set(n.id, []);
    indegree.set(n.id, 0);
  }
  for (const e of edges) {
    const out = adjacency.get(e.from);
    if (out && indegree.has(e.to)) {
      out.push(e.to);
      indegree.set(e.to, (indegree.get(e.to) ?? 0) + 1);
    }
  }

  // Kahn traversal accumulating the longest depth seen for each node.
  const depth = new Map<string, number>();
  const remaining = new Map(indegree);
  const queue: string[] = [];
  for (const n of nodes) {
    if ((indegree.get(n.id) ?? 0) === 0) {
      depth.set(n.id, 0);
      queue.push(n.id);
    }
  }
  // The array iterator visits elements pushed during iteration, so this is a
  // BFS over the whole reachable graph.
  for (const id of queue) {
    for (const next of adjacency.get(id) ?? []) {
      depth.set(next, Math.max(depth.get(next) ?? 0, (depth.get(id) ?? 0) + 1));
      const left = (remaining.get(next) ?? 0) - 1;
      remaining.set(next, left);
      if (left === 0) queue.push(next);
    }
  }

  const byDepth = new Map<number, string[]>();
  for (const n of nodes) {
    const d = depth.get(n.id) ?? 0;
    const row = byDepth.get(d) ?? [];
    row.push(n.id);
    byDepth.set(d, row);
  }
  const positions = new Map<string, XY>();
  for (const [d, ids] of byDepth) {
    ids.forEach((id, row) => positions.set(id, { x: d * COLUMN, y: row * ROW }));
  }
  return positions;
}
