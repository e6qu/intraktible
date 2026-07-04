// SPDX-License-Identifier: AGPL-3.0-or-later
// Pins that the demo's displayed numbers and motion DERIVE from the seeded
// collections rather than being canned: streamed agent chunks reassemble exactly
// to the recorded run, the builder Replay animates the newest decision's actual
// recorded trace, the MRM inventory's validation/monitoring columns are computed
// evidence, and a model no flow scores with reports an honestly-empty drift
// histogram instead of an invented one.

import { describe, it, expect } from 'vitest';
import { handleDemo } from './router';
import { chunksOf, recordStreamRun } from './install';
import { agentReply } from './agent';
import { state } from './store';
import type { Decision, Monitor, MrmReport } from '$lib/api';

const params = (): URLSearchParams => new URLSearchParams();

describe('streaming fidelity: chunks reassemble exactly to the recorded run', () => {
  it('chunksOf concatenates back to the original text, byte for byte', () => {
    const samples = [
      agentReply('sanctions screening on a $50k wire').text,
      agentReply('Wire of $50,000 to a sanctioned region', {
        properties: { narrative: { type: 'string' }, risk_score: { type: 'number' } }
      }).text, // multi-line pretty-printed JSON
      'one',
      'two words',
      'text  with  double  spaces and\nnewlines kept intact'
    ];
    for (const text of samples) {
      const chunks = chunksOf(text);
      expect(chunks.join(''), JSON.stringify(text)).toBe(text);
      expect(chunks.length).toBeGreaterThan(0);
      expect(chunks.length).toBeLessThanOrEqual(5);
    }
  });

  it('recordStreamRun records the streamed reply verbatim — text AND structured', () => {
    const agent = state.agents.find((a) => a.schema);
    if (!agent) throw new Error('no schema-bearing seeded agent');
    const prompt = 'assess this wire for sanctions exposure';
    const reply = agentReply(prompt, agent.schema as { properties?: Record<string, unknown> });
    const runsBefore = agent.runs;
    recordStreamRun(agent.name, prompt, reply);
    const recorded = state.agentRuns[0];
    // What the stream displayed (the reassembled chunks) is exactly what the run
    // list will show for this run — no paraphrase, no trailing separator.
    expect(chunksOf(reply.text).join('')).toBe(reply.text);
    expect(recorded.agent).toBe(agent.name);
    expect(recorded.prompt).toBe(prompt);
    expect(recorded.text).toBe(reply.text);
    expect(recorded.structured).toEqual(reply.structured);
    expect(recorded.status).toBe('completed');
    expect(agent.runs).toBe(runsBefore + 1);
    // And it is served by the runs endpoint like any non-streamed run.
    const listed = handleDemo('GET', `/v1/agents/${agent.name}/runs`, params(), {}).body as {
      runs: { run_id: string; text?: string }[];
    };
    expect(listed.runs[0].run_id).toBe(recorded.run_id);
    expect(listed.runs[0].text).toBe(reply.text);
  });
});

describe('replay fidelity: the newest decision and its recorded trace', () => {
  it('GET /v1/decisions serves decisions newest-first (what Replay picks from)', () => {
    const { decisions } = handleDemo('GET', '/v1/decisions', params(), {}).body as {
      decisions: Decision[];
    };
    expect(decisions.length).toBeGreaterThan(100);
    for (let i = 1; i < decisions.length; i++) {
      expect(
        decisions[i - 1].started_at >= decisions[i].started_at,
        `decisions[${i - 1}] should not be older than decisions[${i}]`
      ).toBe(true);
    }
  });

  it('a fresh decide surfaces first with its actual recorded node path', () => {
    const res = handleDemo('POST', '/v1/flows/card-fraud/sandbox/decide', params(), {
      data: {
        amount: 120,
        tx_count_1h: 1,
        device_score: 15,
        avg_ticket: 120,
        card_present: 1,
        new_device: 0
      }
    });
    const decisionId = (res.body as { decision_id: string }).decision_id;
    const { decisions } = handleDemo('GET', '/v1/decisions', params(), {}).body as {
      decisions: Decision[];
    };
    // Replay filters to the flow and animates decisions[0].nodes in order — that
    // must be THIS decision's recorded trace, not a generic walk.
    const forFlow = decisions.filter((d) => d.slug === 'card-fraud' && (d.nodes?.length ?? 0) > 0);
    expect(forFlow[0].decision_id).toBe(decisionId);
    const recorded = state.decisions.find((d) => d.decision_id === decisionId);
    expect(recorded?.nodes?.length).toBeGreaterThan(0);
    expect(forFlow[0].nodes?.map((n) => n.node_id)).toEqual(recorded?.nodes?.map((n) => n.node_id));
    // Every animated node exists in the graph version the decision recorded.
    const flow = state.flows.find((f) => f.slug === 'card-fraud');
    const graph = flow?.versions.find((v) => v.version === recorded?.version)?.graph;
    const ids = new Set(graph?.nodes.map((n) => n.id));
    for (const n of recorded?.nodes ?? []) expect(ids.has(n.node_id)).toBe(true);
  });
});

describe('MRM inventory derives its columns from live state', () => {
  const report = (): MrmReport =>
    handleDemo('GET', '/v1/mrm/report', params(), {}).body as MrmReport;

  it('flow validation runs the assertion suite: seeded suites pass on the latest version', () => {
    for (const m of report().models.filter((x) => x.kind === 'flow')) {
      const cases = state.assertions.get(m.id) ?? [];
      expect(m.validation.assertions_total).toBe(cases.length);
      if (cases.length === 0) {
        expect(m.validation.coverage).toBe('none');
        expect(m.issues).toContain('No assertions defined');
      } else {
        expect(m.validation.assertions_passed, `${m.id} assertions`).toBe(cases.length);
        expect(m.validation.coverage).toBe('tested');
      }
    }
  });

  it('flow firing monitors match the live monitor evaluation, not the seed status', () => {
    const flow = state.flows.find((f) => f.slug === 'aml-screening');
    if (!flow) throw new Error('seed flow missing');
    const { monitors } = handleDemo('GET', `/v1/flows/${flow.slug}/monitors`, params(), {})
      .body as { monitors: Monitor[] };
    const liveFiring = monitors.filter((m) => m.status.firing).map((m) => m.metric);
    const row = report().models.find((m) => m.kind === 'flow' && m.id === flow.flow_id);
    expect(row?.monitoring.firing_monitors ?? []).toEqual(liveFiring);
    // The seeded AML referral mix genuinely trips the refer-rate monitor — the MRM
    // badge reflects computed data, so this list is non-empty on the fresh seed.
    expect(liveFiring).toContain('refer_rate');
  });

  it('agent success rate is completed over terminal runs from the run records', () => {
    const rows = report().models.filter((m) => m.kind === 'agent');
    expect(rows.length).toBeGreaterThanOrEqual(6);
    let sawFailure = false;
    for (const row of rows) {
      const runs = state.agentRuns.filter((r) => r.agent === row.id);
      const completed = runs.filter((r) => r.status === 'completed').length;
      const terminal = completed + runs.filter((r) => r.status === 'failed').length;
      expect(row.monitoring.decisions).toBe(runs.length);
      expect(row.monitoring.success_rate).toBe(terminal ? completed / terminal : 1);
      if (terminal && completed < terminal) sawFailure = true;
    }
    // The seed carries failed runs, so at least one agent reads below 100%.
    expect(sawFailure).toBe(true);
  });

  it('a failing assertion flips its flow to coverage "failing" with a real pass count', () => {
    const flow = state.flows.find((f) => f.slug === 'card-fraud');
    if (!flow) throw new Error('seed flow missing');
    const original = state.assertions.get(flow.flow_id) ?? [];
    expect(
      handleDemo('PUT', `/v1/flows/${flow.slug}/assertions`, params(), {
        cases: [
          ...original,
          { name: 'impossible', input: { amount: 10 }, expect: { blocked: 'never-this' } }
        ]
      }).status
    ).toBe(200);
    const row = report().models.find((m) => m.kind === 'flow' && m.id === flow.flow_id);
    expect(row?.validation.coverage).toBe('failing');
    expect(row?.validation.assertions_total).toBe(original.length + 1);
    expect(row?.validation.assertions_passed).toBe(original.length);
    expect(row?.issues).toContain('Assertions failing');
    state.assertions.set(flow.flow_id, original);
  });

  it('flow owner is the latest version publisher', () => {
    for (const m of report().models.filter((x) => x.kind === 'flow')) {
      const flow = state.flows.find((f) => f.flow_id === m.id);
      expect(m.owner).toBe(flow?.versions.at(-1)?.published_by);
    }
  });
});

describe('model drift without predictions is honestly empty', () => {
  it('a model no flow scores with reports zero bins and count 0 — never an invented histogram', () => {
    expect(
      handleDemo('POST', '/v1/models', params(), {
        name: 'orphan_expr',
        spec: { kind: 'expression', expression: 'amount / 100' }
      }).status
    ).toBe(200);
    const res = handleDemo('GET', '/v1/models/orphan_expr/drift', params(), {}).body as {
      count: number;
      hist: number[];
      has_baseline: boolean;
      psi?: number;
    };
    expect(res.count).toBe(0);
    expect(res.hist).toEqual([0, 0, 0, 0, 0]);
    expect(res.has_baseline).toBe(false);
    expect(res.psi).toBeUndefined();
  });
});

describe('monitor check delivers to the subscribed webhooks', () => {
  it('deliveries list the active monitor.fired hooks, not a fixed first-hook stub', () => {
    const res = handleDemo('POST', '/v1/flows/aml-screening/monitors/check', params(), {}).body as {
      fired: unknown[];
      deliveries: { webhook_id: string; url: string }[];
    };
    expect(res.fired.length).toBeGreaterThan(0); // the seeded refer mix fires
    const subscribed = state.webhooks.filter(
      (w) => w.active && (w.events ?? []).includes('monitor.fired')
    );
    expect(res.deliveries.map((d) => d.webhook_id)).toEqual(subscribed.map((w) => w.webhook_id));
    for (const d of res.deliveries) {
      expect(subscribed.find((w) => w.webhook_id === d.webhook_id)?.url).toBe(d.url);
    }
  });
});

describe('hello stats are a real projection over the hellos said', () => {
  it('starts at zero and folds every POST /v1/hello', () => {
    interface HelloStatsBody {
      count: number;
      last_name: string;
      last_at: string;
    }
    const stats = (): HelloStatsBody =>
      handleDemo('GET', '/v1/hello/stats', params(), {}).body as HelloStatsBody;
    expect(stats().count).toBe(0); // a fresh workspace has said no hellos
    expect(stats().last_name).toBe('');
    handleDemo('POST', '/v1/hello', params(), { name: 'Ada' });
    handleDemo('POST', '/v1/hello', params(), { name: 'Grace' });
    const s = stats();
    expect(s.count).toBe(2);
    expect(s.last_name).toBe('Grace');
    expect(Number.isNaN(Date.parse(s.last_at))).toBe(false);
    // The command journals into the event log (command → event log → projection).
    expect(state.audit[0].type).toBe('hello.said');
  });
});
