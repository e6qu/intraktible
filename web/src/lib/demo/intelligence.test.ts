// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, it, expect } from 'vitest';
import { state } from './store';
import { pickVersion } from './engine';
import { nodeStats, counterfactual, coverage } from './intelligence';

const flow = state.flows.find((f) => f.slug === 'credit-decision');
if (!flow) throw new Error('seed flow credit-decision missing');

describe('nodeStats', () => {
  it('counts node traversals across the flow decisions', () => {
    const s = nodeStats(flow, state.decisions);
    expect(s.total).toBeGreaterThan(0);
    expect(s.nodes.length).toBeGreaterThan(0);
    // The input node is on every decision's path, so its count equals the total.
    const input = s.nodes.find((n) => n.type === 'input');
    expect(input?.count).toBe(s.total);
    expect(input?.pct).toBeCloseTo(1, 5);
    const dispTotal = s.dispositions.approve + s.dispositions.decline + s.dispositions.refer;
    expect(dispTotal).toBeGreaterThan(0);
  });
});

describe('counterfactual', () => {
  it('returns no flips for an already-approved decision', () => {
    const approved = state.decisions.find(
      (d) => d.flow_id === flow.flow_id && d.disposition === 'approve'
    );
    if (!approved) throw new Error('no approved credit decision in seed');
    const cf = counterfactual(flow, approved);
    expect(cf.flips).toEqual([]);
    expect(cf.searched).toBe(0);
  });

  it('searches well-formed single-field flips for a non-approved decision', () => {
    const declined = state.decisions.find(
      (d) =>
        d.flow_id === flow.flow_id && (d.disposition === 'decline' || d.disposition === 'refer')
    );
    if (!declined) return; // tolerate seed variance
    const cf = counterfactual(flow, declined);
    expect(cf.searched).toBeGreaterThan(0);
    expect(cf.disposition).toBe(declined.disposition);
    const targets = new Set<string>();
    for (const n of flow.versions.at(-1)?.graph.nodes ?? []) {
      for (const m of JSON.stringify(n.config ?? {}).matchAll(/"(?:target|output)":"([^"]*)"/g))
        targets.add(m[1]);
    }
    for (const f of cf.flips) {
      expect(typeof f.field).toBe('string');
      expect(['increase', 'decrease']).toContain(f.direction);
      expect(f.to).not.toBe(f.from); // no degenerate from→from flip
      expect(f.to).toBeGreaterThanOrEqual(0); // no negative income/balance suggestions
      expect(f.direction === 'increase' ? f.to > f.from : f.to < f.from).toBe(true);
      expect(targets.has(f.field)).toBe(false); // no derived field offered as a lever
    }
  });
});

describe('coverage', () => {
  it('fuzzes inputs and reports deterministic node/branch coverage', () => {
    const { graph } = pickVersion(flow, 'production');
    const cov = coverage(flow, graph, 120);
    expect(cov.runs).toBe(120);
    expect(cov.nodes.length).toBe(graph.nodes.length);
    expect(Array.isArray(cov.dead_branches)).toBe(true);
    const dispTotal = cov.dispositions.approve + cov.dispositions.decline + cov.dispositions.refer;
    expect(dispTotal).toBeLessThanOrEqual(cov.runs);
    // Deterministic: the same flow + run count yields identical coverage (seeded fuzz).
    const cov2 = coverage(flow, graph, 120);
    expect(cov2.dispositions).toEqual(cov.dispositions);
    expect(cov2.dead_nodes).toEqual(cov.dead_nodes);
  });

  it('flags a branch that no synthetic input can reach as dead', () => {
    const graph = {
      nodes: [
        { id: 'in', type: 'input' as const },
        { id: 'gate', type: 'split' as const },
        { id: 'out', type: 'output' as const }
      ],
      edges: [
        { from: 'in', to: 'gate' },
        { from: 'gate', to: 'out', branch: 'risk < 50' },
        // Unreachable: contradicts the first branch and is never the chosen path.
        { from: 'gate', to: 'out', branch: 'risk < 0' }
      ]
    };
    const cov = coverage(flow, graph, 80);
    expect(cov.dead_branches.some((b) => b.branch === 'risk < 0')).toBe(true);
  });
});
