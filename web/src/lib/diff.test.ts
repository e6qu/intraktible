// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, it, expect } from 'vitest';
import { diffGraphs, diffIsEmpty, canon, edgeKey, type GraphDiff } from './diff';
import type { FlowGraph } from './api';

const g = (nodes: FlowGraph['nodes'], edges: FlowGraph['edges']): FlowGraph => ({ nodes, edges });

describe('diffGraphs', () => {
  it('detects added, removed, and changed nodes', () => {
    const a = g(
      [
        { id: 'in', type: 'input' },
        { id: 'a', type: 'assignment', config: { x: 1 } },
        { id: 'gone', type: 'output' }
      ],
      []
    );
    const b = g(
      [
        { id: 'in', type: 'input' },
        { id: 'a', type: 'assignment', config: { x: 2 } }, // changed config
        { id: 'new', type: 'output' }
      ],
      []
    );
    const d = diffGraphs(a, b);
    expect(d.nodesAdded).toEqual(['new']);
    expect(d.nodesRemoved).toEqual(['gone']);
    expect(d.nodesChanged).toEqual(['a']);
  });

  it('detects added and removed edges', () => {
    const a = g([], [{ from: 'in', to: 'a' }]);
    const b = g([], [{ from: 'a', to: 'out', branch: 'yes' }]);
    const d = diffGraphs(a, b);
    expect(d.edgesRemoved).toEqual(['in → a']);
    expect(d.edgesAdded).toEqual(['a → out [yes]']);
  });

  it('treats config key order as equal (canonical compare)', () => {
    const a = g([{ id: 'a', type: 'rule', config: { x: 1, y: 2 } }], []);
    const b = g([{ id: 'a', type: 'rule', config: { y: 2, x: 1 } }], []);
    const d: GraphDiff = diffGraphs(a, b);
    expect(diffIsEmpty(d)).toBe(true);
  });

  it('canon is key-order independent; edgeKey includes the branch', () => {
    expect(canon({ b: 1, a: 2 })).toBe(canon({ a: 2, b: 1 }));
    expect(edgeKey({ from: 'x', to: 'y', branch: 'no' })).toBe('x → y [no]');
  });
});
