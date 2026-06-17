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

const LANE_GAP = 48;
const LANE_PAD = 24;

// LaneBand is the rendered extent of one swimlane (canvas coordinates).
export interface LaneBand {
  lane: string;
  top: number;
  height: number;
}

interface LaneNodeRef {
  id: string;
  lane?: string;
}

// layoutLanes lays nodes left-to-right by flow depth (as layout does), but stacks
// them into horizontal swimlanes by their lane — so a flow reads as ordered stages
// within owned lanes. Returns the positions plus each lane's band for drawing.
// Lanes appear in first-seen order; a node with no lane falls into "Main".
export function layoutLanes(
  nodes: LaneNodeRef[],
  edges: EdgeRef[]
): { pos: Map<string, XY>; bands: LaneBand[] } {
  const depthX = layout(nodes, edges); // reuse the depth pass for x only
  const laneOrder: string[] = [];
  const byLane = new Map<string, string[]>();
  for (const n of nodes) {
    const lane = n.lane || 'Main';
    if (!byLane.has(lane)) {
      byLane.set(lane, []);
      laneOrder.push(lane);
    }
    (byLane.get(lane) as string[]).push(n.id);
  }

  const pos = new Map<string, XY>();
  const bands: LaneBand[] = [];
  let cursorY = 0;
  for (const lane of laneOrder) {
    const ids = byLane.get(lane) as string[];
    const rowAtX = new Map<number, number>(); // stack nodes sharing a depth column
    let rows = 0;
    for (const id of ids) {
      const x = depthX.get(id)?.x ?? 0;
      const row = rowAtX.get(x) ?? 0;
      rowAtX.set(x, row + 1);
      rows = Math.max(rows, row + 1);
      pos.set(id, { x, y: cursorY + row * ROW });
    }
    const height = Math.max(rows, 1) * ROW;
    bands.push({ lane, top: cursorY - LANE_PAD, height: height + LANE_PAD });
    cursorY += height + LANE_GAP;
  }
  return { pos, bands };
}
