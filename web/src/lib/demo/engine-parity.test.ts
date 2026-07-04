// SPDX-License-Identifier: AGPL-3.0-or-later
// The differential proof that the demo's in-browser engine IS the real engine
// behaviorally: every case in engine-parity-fixtures.json was computed by the REAL
// decision engine (domain.Execute, via decision-engine/domain/parity_fixtures_test.go)
// and is replayed here through the demo walker, asserting status, final output,
// trace node-id sequence, per-node outputs, reason codes, failed node, suspend
// state and (for failures) that the demo error CONTAINS the engine's core message.
// The Go test fails loudly when the committed fixtures drift from what the engine
// computes; this suite fails loudly when the demo drifts from the fixtures.

import { readFileSync } from 'node:fs';
import { describe, it, expect } from 'vitest';
import { walkGraph } from './walk';
import type { FlowGraph } from '$lib/api';

interface ParityTraceNode {
  node_id: string;
  type: string;
  output: unknown;
}

interface ParityExpect {
  status: 'completed' | 'failed' | 'suspended';
  output?: Record<string, unknown>;
  reason_codes?: unknown[];
  trace: ParityTraceNode[];
  failed_node?: string;
  error?: string;
  error_contains?: string;
  suspend?: {
    node_id: string;
    record: Record<string, unknown>;
    company_name: string;
    case_type: string;
    sla_days: number;
  };
}

interface ParityCase {
  name: string;
  graph: FlowGraph;
  input: Record<string, unknown>;
  expect: ParityExpect;
}

interface ParityFile {
  _readme: string[];
  cases: ParityCase[];
}

const fixtures = JSON.parse(
  readFileSync(new URL('./engine-parity-fixtures.json', import.meta.url), 'utf8')
) as ParityFile;

// norm JSON-normalizes a demo value for comparison against a fixture value (which
// already went through Go's encoding/json): undefined becomes null, and float
// representation is IEEE-754 double in both runtimes so numbers compare exactly.
function norm(v: unknown): unknown {
  if (v === undefined) return null;
  return JSON.parse(JSON.stringify(v));
}

describe('demo engine parity with decision-engine domain.Execute', () => {
  it('carries a real battery', () => {
    expect(fixtures.cases.length).toBeGreaterThanOrEqual(75);
  });

  for (const c of fixtures.cases) {
    it(c.name, () => {
      // Models are deliberately empty: predict/connect/ai cases carry pre-resolved
      // buckets (the shell seam), so the walker must echo them, never re-resolve.
      const run = walkGraph(c.graph, c.input, []);

      expect(run.status, 'run status').toBe(c.expect.status);
      expect(
        run.nodes.map((n) => n.node_id),
        'trace node-id sequence'
      ).toEqual(c.expect.trace.map((t) => t.node_id));
      expect(
        run.nodes.map((n) => String(n.type)),
        'trace node types'
      ).toEqual(c.expect.trace.map((t) => t.type));
      run.nodes.forEach((n, i) => {
        expect(norm(n.output), `node ${n.node_id} output`).toEqual(
          c.expect.trace.at(i)?.output ?? null
        );
      });

      if (c.expect.status === 'completed') {
        expect(norm(run.output), 'final output').toEqual(c.expect.output);
        expect(norm(run.reasonCodes), 'reason codes').toEqual(c.expect.reason_codes ?? []);
      }
      if (c.expect.status === 'failed') {
        expect(run.error, 'failed run must carry an error').toBeTruthy();
        expect(run.failedNode ?? '', 'failed node').toBe(c.expect.failed_node ?? '');
        expect(run.error, 'error core message').toContain(c.expect.error_contains ?? '');
      }
      if (c.expect.status === 'suspended') {
        const s = c.expect.suspend;
        if (!s) throw new Error(`fixture ${c.name} is suspended without suspend state`);
        expect(run.suspend?.node_id, 'suspend node').toBe(s.node_id);
        expect(norm(run.data), 'suspended record').toEqual(s.record);
        expect(run.caseOpened?.case_type, 'case type').toBe(s.case_type);
        expect(run.caseOpened?.sla_days, 'case sla').toBe(s.sla_days);
        expect(run.caseOpened?.company_name, 'case company').toBe(s.company_name);
        expect(norm(run.reasonCodes), 'reason codes').toEqual(c.expect.reason_codes ?? []);
      }
    });
  }
});
