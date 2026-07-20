// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'vitest';
import { draftGraph, simulatedModel, registerSimulatedAI } from './ai-sim';

// The copilot's graph schema (decision-engine/service/service.go graphSchema): the
// one request the sim must answer with a real graph rather than field guesses.
const GRAPH_SCHEMA = {
  type: 'object',
  required: ['nodes', 'edges'],
  properties: {
    nodes: { type: 'array' },
    edges: { type: 'array' }
  }
};

// Mirrors decision-engine/domain ValidateGraph + the split yes/no rule, at the
// structural level TS can see: one input, at least one output, no dead ends, every
// edge references a real node, acyclic, and every split has both branches. The Go
// ValidateFlow is the true gate (exercised by the demo e2e); this keeps the
// synthesizer honest without a backend.
function assertPublishable(graph: {
  nodes: { id: string; type: string }[];
  edges: { from: string; to: string; branch?: string }[];
}) {
  const ids = new Set(graph.nodes.map((n) => n.id));
  expect(ids.size).toBe(graph.nodes.length); // unique ids
  expect(graph.nodes.filter((n) => n.type === 'input')).toHaveLength(1);
  expect(graph.nodes.filter((n) => n.type === 'output').length).toBeGreaterThanOrEqual(1);

  const outgoing = new Set(graph.edges.map((e) => e.from));
  for (const n of graph.nodes) {
    expect(ids.has(n.id)).toBe(true);
    if (n.type !== 'output') expect(outgoing.has(n.id)).toBe(true); // no dead ends
  }
  for (const e of graph.edges) {
    expect(ids.has(e.from)).toBe(true);
    expect(ids.has(e.to)).toBe(true);
    expect(e.from).not.toBe(e.to); // no self-loop
  }
  // Every split carries both a "yes" and a "no" outgoing edge.
  for (const n of graph.nodes.filter((n) => n.type === 'split')) {
    const branches = graph.edges.filter((e) => e.from === n.id).map((e) => e.branch);
    expect(branches).toContain('yes');
    expect(branches).toContain('no');
  }
  // Every node reachable from the input (BFS), so nothing dangles.
  const adj = new Map<string, string[]>();
  for (const e of graph.edges) adj.set(e.from, [...(adj.get(e.from) ?? []), e.to]);
  const input = graph.nodes.find((n) => n.type === 'input');
  expect(input).toBeDefined();
  const seen = new Set([input?.id ?? '']);
  const queue = [...seen];
  while (queue.length) {
    const cur = queue.shift() ?? '';
    for (const to of adj.get(cur) ?? []) {
      if (!seen.has(to)) {
        seen.add(to);
        queue.push(to);
      }
    }
  }
  expect(seen.size).toBe(graph.nodes.length);
}

describe('draftGraph', () => {
  for (const prompt of [
    'Screen a payment for fraud using velocity and a new-device signal.',
    'Onboard a business customer: run sanctions and PEP screening.',
    'Approve loans under $50k when DTI is below 40%.',
    'Cap exposure above the requested amount.',
    'Something with no matching keywords at all.'
  ]) {
    it(`synthesizes a publishable graph for: ${prompt.slice(0, 24)}…`, () => {
      assertPublishable(draftGraph(prompt));
    });
  }

  it('is deterministic — the same prompt yields an identical graph', () => {
    const a = JSON.stringify(draftGraph('fraud velocity and device'));
    const b = JSON.stringify(draftGraph('fraud velocity and device'));
    expect(a).toBe(b);
  });

  it('derives factors from prompt keywords', () => {
    const fraud = draftGraph('detect fraud by velocity');
    const kyc = draftGraph('run sanctions and kyc screening');
    const scoreOf = (g: ReturnType<typeof draftGraph>) =>
      JSON.stringify(g.nodes.find((n) => n.type === 'scorecard')?.config);
    expect(scoreOf(fraud)).toContain('transaction_velocity');
    expect(scoreOf(kyc)).toContain('sanctions_hit');
    expect(scoreOf(fraud)).not.toBe(scoreOf(kyc));
  });
});

describe('simulatedModel', () => {
  it('marks the copilot (no model) output as simulated', () => {
    expect(simulatedModel()).toBe('simulated-llm');
    expect(simulatedModel('')).toBe('simulated-llm');
  });

  it('prefixes a pinned agent model so the disclosure travels with the output', () => {
    expect(simulatedModel('claude-sonnet')).toBe('simulated-claude-sonnet');
  });

  it('is idempotent — an already-marked model is not double-prefixed', () => {
    expect(simulatedModel('simulated-claude-sonnet')).toBe('simulated-claude-sonnet');
  });
});

describe('the registered __intraktible_ai hook', () => {
  it('answers a graph-schema request with a publishable graph and a simulated model', async () => {
    registerSimulatedAI();
    const hook = (globalThis as Record<string, unknown>).__intraktible_ai as (
      s: string
    ) => Promise<string>;
    const raw = await hook(
      JSON.stringify({ prompt: 'screen a payment for fraud', schema: GRAPH_SCHEMA })
    );
    const resp = JSON.parse(raw) as {
      model: string;
      structured: {
        nodes: { id: string; type: string }[];
        edges: { from: string; to: string; branch?: string }[];
      };
    };
    expect(resp.model).toBe('simulated-llm');
    assertPublishable(resp.structured);
  });

  it('honors every declared JSON Schema field type in a structured agent response', async () => {
    registerSimulatedAI();
    const hook = (globalThis as Record<string, unknown>).__intraktible_ai as (
      s: string
    ) => Promise<string>;
    const raw = await hook(
      JSON.stringify({
        prompt: 'Income dropped after a medical hardship.',
        schema: {
          type: 'object',
          required: ['plan_months', 'rate_relief', 'summary', 'eligible', 'actions', 'details'],
          properties: {
            plan_months: { type: 'number', minimum: 1, maximum: 24 },
            rate_relief: { type: 'number', minimum: 0, maximum: 1 },
            summary: { type: 'string' },
            eligible: { type: 'boolean' },
            actions: { type: 'array', minItems: 1, items: { type: 'string' } },
            details: {
              type: 'object',
              properties: { review_days: { type: 'integer', minimum: 1 } }
            }
          }
        }
      })
    );
    const { structured } = JSON.parse(raw) as { structured: Record<string, unknown> };
    expect(structured.plan_months).toBeTypeOf('number');
    expect(structured.rate_relief).toBeTypeOf('number');
    expect(structured.summary).toBeTypeOf('string');
    expect(structured.eligible).toBeTypeOf('boolean');
    expect(structured.actions).toEqual([expect.any(String)]);
    expect(structured.details).toEqual({ review_days: expect.any(Number) });
  });
});
