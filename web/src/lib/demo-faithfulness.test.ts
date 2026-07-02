// SPDX-License-Identifier: AGPL-3.0-or-later
// Guards the demo-faithfulness behaviors: the agent reply is a plausible response
// (never the "stub: <prompt>" echo), a preview decide records nothing, and the
// admin-only surfaces gate on the switched user's role (matching the real backend).

import { describe, it, expect, afterEach } from 'vitest';
import { agentReply } from './demo/agent';
import { evaluateModel, runFlow, setRollPercent } from './demo/engine';
import type { Decision, FlowGraph, Model } from './api';
import { handleDemo } from './demo/router';
import { setDemoUser, state, USERS, psi } from './demo/store';

const params = (): URLSearchParams => new URLSearchParams();

describe('agentReply', () => {
  it('returns a structured JSON verdict for a schema-bearing agent — no stub echo', () => {
    const r = agentReply('Wire of $50,000 to a sanctioned region', {
      properties: { narrative: { type: 'string' } }
    });
    expect(r.text).toContain('narrative');
    expect(r.text).not.toContain('stub:');
    expect(r.structured?.narrative).toBeTruthy();
  });

  it('returns a prompt-shaped narrative when the agent has no schema', () => {
    expect(agentReply('sanctions screening on a $50k wire').text.toLowerCase()).toContain(
      'recommend'
    );
    expect(agentReply('anything at all').text).not.toContain('stub:');
  });
});

describe('demo decide preview', () => {
  it('computes a result but records no decision', () => {
    const before = state.decisions.length;
    const res = handleDemo('POST', '/v1/flows/card-fraud/sandbox/decide', params(), {
      data: { amount: 100 },
      preview: true
    });
    expect(res.status).toBe(200);
    expect((res.body as { decision_id: string }).decision_id).toBe('');
    expect(state.decisions.length).toBe(before);
  });

  it('records a decision on a normal (non-preview) run', () => {
    const before = state.decisions.length;
    handleDemo('POST', '/v1/flows/card-fraud/sandbox/decide', params(), { data: { amount: 100 } });
    expect(state.decisions.length).toBe(before + 1);
  });
});

describe('pre-approval honoring', () => {
  it('serves a matching active grant instantly and increments its honored count', () => {
    const grant = state.preapprovals.find((g) => g.preapproval_id === 'pa_1');
    const before = grant?.honored_count ?? 0;
    const decisionsBefore = state.decisions.length;
    const res = handleDemo('POST', '/v1/flows/credit-decision/sandbox/decide', params(), {
      data: { amount: 5000 },
      entity_type: 'applicant',
      entity_id: 'APP-1001'
    });
    expect(res.status).toBe(200);
    const body = res.body as {
      disposition: string;
      disposition_reason: string;
      preapproval_id: string;
    };
    expect(body.disposition).toBe('approve');
    // The honored fast path mirrors the real decide response: the grant id plus the
    // literal disposition_reason the backend uses.
    expect(body.preapproval_id).toBe('pa_1');
    expect(body.disposition_reason).toBe('pre-approval honored');
    expect(grant?.honored_count).toBe(before + 1);
    // a decision is recorded, referencing the grant
    expect(state.decisions.length).toBe(decisionsBefore + 1);
    expect(state.decisions[0].preapproval_id).toBe('pa_1');
  });

  it('does not short-circuit when no grant matches the entity', () => {
    const res = handleDemo('POST', '/v1/flows/credit-decision/sandbox/decide', params(), {
      data: { amount: 5000 },
      entity_type: 'applicant',
      entity_id: 'NO-SUCH-ENTITY'
    });
    expect(state.decisions[0].preapproval_id).toBeUndefined();
    expect(res.status).toBe(200);
  });
});

describe('PSI drift', () => {
  it('is zero for identical distributions and positive for a shifted one', () => {
    expect(psi([25, 25, 25, 25], [25, 25, 25, 25])).toBe(0);
    expect(psi([50, 50], [10, 90])).toBeGreaterThan(0);
  });

  it('computes drift from the model real predictions (bounded, not a constant)', () => {
    // Capture a baseline, then the drift PSI is a real, finite, believable value derived
    // from the model's predictions over recorded decisions — not a fixed constant.
    handleDemo('POST', '/v1/models/credit_pd/baseline', params(), {});
    const res = handleDemo('GET', '/v1/models/credit_pd/drift', params(), {}) as {
      body: { psi?: number; has_baseline: boolean; count: number; hist: number[] };
    };
    expect(res.body.has_baseline).toBe(true);
    expect(typeof res.body.psi).toBe('number');
    expect(Number.isFinite(res.body.psi)).toBe(true);
    expect(res.body.psi).toBeLessThan(5); // smoothed — never an absurd double-digit PSI
    expect(res.body.hist.reduce((a, b) => a + b, 0)).toBe(res.body.count);
    const missing = handleDemo('GET', '/v1/models/no-such-model/drift', params(), {}) as {
      status: number;
    };
    expect(missing.status).toBe(404);
  });
});

describe('seeded cases reference a coherent source decision', () => {
  // Each case opens FROM a review-worthy (refer), non-failed decision of the SAME
  // flow as the case, so the "source decision" link always lands on a real trace
  // that explains why a human is in the loop.
  const caseTypeToFlow = new Map<string, string>([
    ['credit_review', 'credit-decision'],
    ['aml_alert', 'aml-screening'],
    ['fraud_review', 'card-fraud'],
    ['kyc_review', 'kyc-onboarding'],
    ['dispute', 'dispute-triage'],
    ['merchant_review', 'merchant-onboarding']
  ]);

  it('every case src is a same-flow, refer, non-failed decision', () => {
    const byId = new Map(state.decisions.map((d) => [d.decision_id, d]));
    for (const c of state.cases) {
      expect(c.source_decision_id, `${c.case_id} should have a source decision`).toBeTruthy();
      const dec = byId.get(c.source_decision_id ?? '');
      expect(dec, `${c.case_id} → ${c.source_decision_id} should resolve`).toBeDefined();
      expect(dec?.slug).toBe(caseTypeToFlow.get(c.case_type));
      expect(dec?.disposition).toBe('refer');
      expect(dec?.status).not.toBe('failed');
    }
  });

  it('each referred decision links back to a case via case_id', () => {
    // The reverse link (decision.case_id) is what the trace page renders to jump to
    // the opened case — every seeded case's source decision must carry it.
    const byId = new Map(state.decisions.map((d) => [d.decision_id, d]));
    for (const c of state.cases) {
      const dec = byId.get(c.source_decision_id ?? '');
      expect(dec?.case_id, `${c.source_decision_id} should link to a case`).toBeTruthy();
    }
    const linked = state.decisions.filter((d) => d.case_id);
    expect(linked.length).toBeGreaterThan(0);
  });

  it('the source decision id in each case audit trail matches its src', () => {
    for (const c of state.cases) {
      const opened = c.audit.find((a) => a.detail?.startsWith('from decision'));
      if (opened) expect(opened.detail).toBe(`from decision ${c.source_decision_id}`);
    }
  });

  it('every non-failed seeded decision carries reason codes', () => {
    const seeded = state.decisions.filter((d) => d.decision_id.startsWith('dec_'));
    expect(seeded.length).toBeGreaterThan(0);
    for (const d of seeded) {
      if (d.status === 'failed') continue;
      expect(
        (d.reason_codes ?? []).length,
        `${d.decision_id} should have a reason code`
      ).toBeGreaterThan(0);
    }
  });
});

describe('create-flow validation', () => {
  it('rejects an empty/whitespace slug and a duplicate slug', () => {
    const before = state.flows.length;
    expect(handleDemo('POST', '/v1/flows', params(), { slug: '   ' }).status).toBe(400);
    expect(handleDemo('POST', '/v1/flows', params(), { slug: 'credit-decision' }).status).toBe(400);
    expect(state.flows.length).toBe(before);
  });

  it('creates a flow for a fresh, non-empty slug', () => {
    const before = state.flows.length;
    const res = handleDemo('POST', '/v1/flows', params(), { slug: 'new-unique-flow' });
    expect(res.status).toBe(200);
    expect(state.flows.length).toBe(before + 1);
  });
});

describe('agent escalation carries run context', () => {
  it('opens an agent_review case populated from the escalated run', () => {
    const agent = state.agents[0].name;
    const run = handleDemo('POST', `/v1/agents/${agent}/run`, params(), {
      prompt: 'score this transaction'
    });
    const runId = (run.body as { run_id: string }).run_id;
    const res = handleDemo('POST', `/v1/agents/${agent}/runs/${runId}/escalate`, params(), {
      case_type: 'agent_review',
      sla_days: 3
    });
    expect(res.status).toBe(200);
    const caseId = (res.body as { case_id: string }).case_id;
    const opened = state.cases.find((c) => c.case_id === caseId);
    expect(opened?.case_type).toBe('agent_review');
    const ctx = opened?.context as Record<string, unknown> | undefined;
    expect(ctx?.run_id).toBe(runId);
    expect(ctx?.prompt).toBe('score this transaction');
    expect(ctx?.output).toBeTruthy();
  });

  it('404s an escalate for a missing agent or unknown run id, opening no case', () => {
    const agent = state.agents[0].name;
    const before = state.cases.length;
    expect(
      handleDemo('POST', '/v1/agents/no-such-agent/runs/run_x/escalate', params(), {}).status
    ).toBe(404);
    expect(
      handleDemo('POST', `/v1/agents/${agent}/runs/run_does_not_exist/escalate`, params(), {})
        .status
    ).toBe(404);
    expect(state.cases.length).toBe(before);
  });
});

describe('deployment request decide guards', () => {
  // Approve/reject must be one-shot: re-deciding an already-decided request would
  // overwrite decided_by/decided_at and push a duplicate audit entry.
  function freshRequest(): string {
    setDemoUser(USERS[2].actor); // Priya — editor, proposes
    const flow = state.flows[0];
    const version = flow.latest;
    const res = handleDemo('POST', `/v1/flows/${flow.slug}/deployment-requests`, params(), {
      environment: 'production',
      version
    });
    return (res.body as { request_id: string }).request_id;
  }

  it('rejects a second approve on an already-approved request', () => {
    const rid = freshRequest();
    const flow = state.flows[0];
    setDemoUser(USERS[1].actor); // Marcus — approver, decides
    expect(
      handleDemo('POST', `/v1/flows/${flow.slug}/deployment-requests/${rid}/approve`, params(), {})
        .status
    ).toBe(200);
    expect(
      handleDemo('POST', `/v1/flows/${flow.slug}/deployment-requests/${rid}/approve`, params(), {})
        .status
    ).toBe(400);
  });

  it('rejects approving an already-rejected request', () => {
    const rid = freshRequest();
    const flow = state.flows[0];
    setDemoUser(USERS[1].actor); // Marcus — approver
    expect(
      handleDemo('POST', `/v1/flows/${flow.slug}/deployment-requests/${rid}/reject`, params(), {})
        .status
    ).toBe(200);
    expect(
      handleDemo('POST', `/v1/flows/${flow.slug}/deployment-requests/${rid}/approve`, params(), {})
        .status
    ).toBe(400);
  });
});

describe('logistic model NaN guard', () => {
  const model: Model = {
    name: 'test_logit',
    kind: 'logistic',
    spec: { kind: 'logistic', intercept: 0.5, coefficients: { a: 2, b: -1 } },
    updated_at: new Date().toISOString()
  };

  it('produces a finite score when a feature is missing (skips that term)', () => {
    const p = evaluateModel(model, { a: 1 });
    expect(Number.isFinite(p.score)).toBe(true);
    expect(Number.isFinite(p.probability ?? NaN)).toBe(true);
    // intercept 0.5 + 2*1, with the missing 'b' term skipped
    expect(p.score).toBe(2.5);
  });

  it('is unchanged when every feature is present', () => {
    const p = evaluateModel(model, { a: 1, b: 3 });
    expect(p.score).toBe(0.5 + 2 * 1 + -1 * 3);
  });
});

describe('admin-only surfaces gate on role', () => {
  it('403s the MRM report for a non-admin and 200s for an admin', () => {
    setDemoUser('lena.hoff@intraktible.dev'); // viewer
    expect(handleDemo('GET', '/v1/mrm/report', params(), {}).status).toBe(403);
    expect(handleDemo('GET', '/v1/audit', params(), {}).status).toBe(403);
    expect(handleDemo('GET', '/v1/api-keys', params(), {}).status).toBe(403);
    setDemoUser(USERS[0].actor); // Ava — admin
    expect(handleDemo('GET', '/v1/mrm/report', params(), {}).status).toBe(200);
    expect(handleDemo('GET', '/v1/audit', params(), {}).status).toBe(200);
  });
});

describe('audit filter: resource and time bounds', () => {
  interface AuditBody {
    entries: { time: string; stream: string; type: string; payload?: unknown }[];
    total: number;
  }

  it('scopes the trail (and its CSV export) to one resource id', () => {
    setDemoUser(USERS[0].actor); // admin
    const created = handleDemo('POST', '/v1/flows', params(), { slug: 'audit-scope-flow' });
    const flowId = (created.body as { flow_id: string }).flow_id;
    // By id: streams are keyed by name (decision.flows), the flow id lives in the payload.
    const byId = handleDemo('GET', '/v1/audit', new URLSearchParams({ resource: flowId }), {})
      .body as AuditBody;
    expect(byId.total).toBeGreaterThan(0);
    for (const e of byId.entries) {
      expect(e.stream).toBe('decision.flows');
      expect(JSON.stringify(e.payload)).toContain(flowId);
    }
    // By a payload value (the slug), matching the real payloadReferences semantics.
    const bySlug = handleDemo(
      'GET',
      '/v1/audit',
      new URLSearchParams({ resource: 'audit-scope-flow' }),
      {}
    ).body as AuditBody;
    expect(bySlug.total).toBeGreaterThan(0);
    // The CSV export applies the same filter: header + one row per matched entry.
    const csv = handleDemo(
      'GET',
      '/v1/audit',
      new URLSearchParams({ resource: flowId, format: 'csv' }),
      {}
    );
    expect(csv.text?.split('\n').length).toBe(1 + byId.total);
  });

  it('applies inclusive since/until bounds and 400s an unparseable one', () => {
    setDemoUser(USERS[0].actor);
    const all = handleDemo('GET', '/v1/audit', params(), {}).body as AuditBody;
    const cutoff = new Date(Date.now() - 3600_000).toISOString(); // one hour ago
    const recent = handleDemo('GET', '/v1/audit', new URLSearchParams({ since: cutoff }), {})
      .body as AuditBody;
    expect(recent.total).toBeGreaterThan(0); // entries this test file just produced
    expect(recent.total).toBeLessThan(all.total); // the seeded hours-old history drops out
    for (const e of recent.entries) {
      expect(Date.parse(e.time)).toBeGreaterThanOrEqual(Date.parse(cutoff));
    }
    const old = handleDemo('GET', '/v1/audit', new URLSearchParams({ until: cutoff }), {})
      .body as AuditBody;
    expect(old.total).toBe(all.total - recent.total);
    expect(
      handleDemo('GET', '/v1/audit', new URLSearchParams({ since: 'not-a-time' }), {}).status
    ).toBe(400);
  });
});

describe('audit log speaks the real event taxonomy', () => {
  interface AuditBody {
    entries: { stream: string; type: string; payload?: unknown }[];
    total: number;
  }

  it('a decide journals started + one node_evaluated per trace node + completed on decision.runs', () => {
    setDemoUser(USERS[0].actor); // admin — the audit endpoint gates on it
    const res = handleDemo('POST', '/v1/flows/card-fraud/sandbox/decide', params(), {
      data: { amount: 120 }
    });
    const decisionId = (res.body as { decision_id: string }).decision_id;
    const decision = state.decisions.find((d) => d.decision_id === decisionId);
    if (!decision) throw new Error('decision not recorded');
    const trail = handleDemo('GET', '/v1/audit', new URLSearchParams({ resource: decisionId }), {})
      .body as AuditBody;
    for (const e of trail.entries) expect(e.stream).toBe('decision.runs');
    const types = trail.entries.map((e) => e.type);
    expect(types.filter((t) => t === 'decision.run.started')).toHaveLength(1);
    expect(types.filter((t) => t === 'decision.run.completed')).toHaveLength(1);
    expect(decision.nodes?.length).toBeGreaterThan(0);
    expect(types.filter((t) => t === 'decision.run.node_evaluated')).toHaveLength(
      decision.nodes?.length ?? 0
    );
  });

  it('exclude_type=decision.run.node_evaluated actually removes rows (the Hide-node-steps toggle)', () => {
    setDemoUser(USERS[0].actor);
    const all = handleDemo('GET', '/v1/audit', params(), {}).body as AuditBody;
    const hidden = handleDemo(
      'GET',
      '/v1/audit',
      new URLSearchParams({ exclude_type: 'decision.run.node_evaluated' }),
      {}
    ).body as AuditBody;
    expect(hidden.total).toBeLessThan(all.total);
    expect(hidden.entries.some((e) => e.type === 'decision.run.node_evaluated')).toBe(false);
  });

  it('the seed already journals node steps for recent decisions (visible on first load)', () => {
    setDemoUser(USERS[0].actor);
    const trail = handleDemo('GET', '/v1/audit', new URLSearchParams({ resource: 'dec_1' }), {})
      .body as AuditBody;
    expect(trail.entries.some((e) => e.type === 'decision.run.node_evaluated')).toBe(true);
    expect(trail.entries.some((e) => e.type === 'decision.run.started')).toBe(true);
  });
});

describe('agent run summary carries computed AI cost', () => {
  it('prices the seeded token usage like a deployment with INTRAKTIBLE_AI_PRICES set', () => {
    const res = handleDemo('GET', '/v1/agent-runs/summary', params(), {});
    const body = res.body as {
      priced: boolean;
      total_cost_usd: number;
      cost_by_model: Record<string, number>;
      by_model: Record<string, { prompt_tokens: number; completion_tokens: number }>;
    };
    expect(body.priced).toBe(true);
    expect(body.total_cost_usd).toBeGreaterThan(0);
    // Recompute from the summary's own token counts × the demo price table — the
    // exact formula Pricing.Cost applies (USD per million tokens, input/output split).
    const rates = new Map([
      ['claude-sonnet', { input: 3, output: 15 }],
      ['claude-haiku', { input: 0.8, output: 4 }]
    ]);
    let expected = 0;
    for (const [model, usage] of Object.entries(body.by_model)) {
      const rate = rates.get(model);
      if (!rate) {
        expect(new Map(Object.entries(body.cost_by_model)).has(model)).toBe(false);
        continue;
      }
      const cost =
        (usage.prompt_tokens / 1e6) * rate.input + (usage.completion_tokens / 1e6) * rate.output;
      expect(new Map(Object.entries(body.cost_by_model)).get(model)).toBeCloseTo(cost, 10);
      expected += cost;
    }
    expect(body.total_cost_usd).toBeCloseTo(expected, 10);
  });
});

describe('flow import upsert', () => {
  it('re-importing an identical export is a no-op (created:false, published:false)', () => {
    const flow = state.flows.find((f) => f.slug === 'credit-decision');
    const latest = flow?.versions.find((v) => v.version === flow.latest);
    const before = flow?.versions.length ?? 0;
    const res = handleDemo('POST', '/v1/flows/import', params(), {
      slug: 'credit-decision',
      graph: latest?.graph,
      input_schema: latest?.input_schema
    });
    expect(res.status).toBe(200);
    const body = res.body as { created: boolean; published: boolean; version: number };
    expect(body.created).toBe(false);
    expect(body.published).toBe(false);
    expect(body.version).toBe(latest?.version);
    expect(flow?.versions.length).toBe(before);
  });

  it('publishes an edited graph onto the existing slug, carrying input_schema through', () => {
    const flow = state.flows.find((f) => f.slug === 'credit-decision');
    if (!flow) throw new Error('seed flow missing');
    const latest = flow.versions.find((v) => v.version === flow.latest);
    if (!latest) throw new Error('latest version missing');
    const edited: FlowGraph = {
      nodes: [
        ...latest.graph.nodes,
        {
          id: 'extra',
          type: 'assignment',
          name: 'Extra',
          config: { assignments: [{ target: 'x', expr: '1' }] }
        }
      ],
      edges: latest.graph.edges
    };
    const res = handleDemo('POST', '/v1/flows/import', params(), {
      slug: 'credit-decision',
      graph: edited,
      input_schema: latest.input_schema
    });
    const body = res.body as { created: boolean; published: boolean; version: number };
    expect(body.created).toBe(false);
    expect(body.published).toBe(true);
    expect(body.version).toBe(flow.latest);
    expect(flow.versions.at(-1)?.input_schema).toEqual(latest.input_schema);
  });

  it('bundle: an existing slug with a different graph counts as updated, not unchanged', () => {
    const flow = state.flows.find((f) => f.slug === 'credit-decision');
    if (!flow) throw new Error('seed flow missing');
    const latest = flow.versions.find((v) => v.version === flow.latest);
    if (!latest) throw new Error('latest version missing');
    const changed: FlowGraph = {
      nodes: [
        { id: 'in', type: 'input', name: 'Input' },
        {
          id: 'out',
          type: 'output',
          name: 'Out',
          config: { assignments: [{ target: 'ok', expr: 'true' }] }
        }
      ],
      edges: [{ from: 'in', to: 'out' }]
    };
    const res = handleDemo('POST', '/v1/flows/import-bundle', params(), {
      flows: [
        { slug: 'credit-decision', graph: latest.graph, input_schema: latest.input_schema },
        { slug: 'credit-decision', graph: changed },
        { slug: '' }
      ]
    });
    const body = res.body as {
      published: number;
      unchanged: number;
      failed: number;
      results: { published: boolean; error?: string }[];
    };
    expect(body.unchanged).toBe(1);
    expect(body.published).toBe(1);
    expect(body.failed).toBe(1);
    expect(body.results).toHaveLength(3);
    expect(body.results[2].error).toBeTruthy();
  });
});

describe('publish etag covers input_schema', () => {
  it('a schema-only change publishes a new version; identical graph+schema no-ops', () => {
    const flow = state.flows.find((f) => f.slug === 'kyc-onboarding');
    if (!flow) throw new Error('seed flow missing');
    const latest = flow.versions.find((v) => v.version === flow.latest);
    if (!latest) throw new Error('latest version missing');
    const noop = handleDemo('POST', `/v1/flows/${flow.slug}/versions`, params(), {
      graph: latest.graph,
      input_schema: latest.input_schema
    });
    expect((noop.body as { published: boolean }).published).toBe(false);
    const bumped = handleDemo('POST', `/v1/flows/${flow.slug}/versions`, params(), {
      graph: latest.graph,
      input_schema: { type: 'object', properties: { extra_flag: { type: 'boolean' } } }
    });
    const body = bumped.body as { published: boolean; version: number };
    expect(body.published).toBe(true);
    expect(body.version).toBe(latest.version + 1);
  });
});

describe('batch per-row input validation', () => {
  afterEach(() => setRollPercent(null));

  it('rejects a wrong-typed row with {index, status, error} and a real rejected count', () => {
    setRollPercent(() => 99); // pin the champion so the valid row is deterministic
    const before = state.decisions.length;
    const res = handleDemo('POST', '/v1/flows/credit-decision/sandbox/decide/batch', params(), {
      dataset: [
        { income: 52000, debt: 14000 },
        { income: 'lots', debt: 14000 }
      ]
    });
    const body = res.body as {
      total: number;
      rejected: number;
      results: { index: number; status: string; error?: string; decision_id?: string }[];
    };
    expect(body.total).toBe(2);
    expect(body.rejected).toBe(1);
    expect(body.results[1].status).toBe('rejected');
    expect(body.results[1].error).toContain('income');
    expect(body.results[1].decision_id).toBeUndefined();
    expect(state.decisions.length).toBe(before + 1); // only the valid row records
  });

  it('preapprove/batch rejects wrong-typed rows the same way, granting nothing', () => {
    setRollPercent(() => 99);
    const before = state.preapprovals.length;
    const res = handleDemo('POST', '/v1/flows/credit-decision/sandbox/preapprove/batch', params(), {
      dataset: [{ id: 'X-1', income: 'lots' }],
      entity_type: 'applicant',
      entity_key: 'id'
    });
    const body = res.body as {
      rejected: number;
      granted: number;
      results: { status: string; granted: boolean }[];
    };
    expect(body.rejected).toBe(1);
    expect(body.granted).toBe(0);
    expect(body.results[0].status).toBe('rejected');
    expect(body.results[0].granted).toBe(false);
    expect(state.preapprovals.length).toBe(before);
  });
});

describe('champion/challenger traffic split', () => {
  // credit-decision's sandbox deployment: champion v3, challenger v2 at 20%.
  afterEach(() => setRollPercent(null));

  it('routes a roll under challenger_pct to the challenger version', () => {
    setRollPercent(() => 0);
    handleDemo('POST', '/v1/flows/credit-decision/sandbox/decide', params(), {
      data: { income: 52000, debt: 14000 }
    });
    expect(state.decisions[0].variant).toBe('challenger');
    expect(state.decisions[0].version).toBe(2);
  });

  it('routes a roll at/above challenger_pct to the champion', () => {
    setRollPercent(() => 20);
    handleDemo('POST', '/v1/flows/credit-decision/sandbox/decide', params(), {
      data: { income: 52000, debt: 14000 }
    });
    expect(state.decisions[0].variant).toBe('champion');
    expect(state.decisions[0].version).toBe(3);
  });
});

describe('runFlow step bound', () => {
  it('fails loudly when a cyclic graph exhausts the bound (never silent completion)', () => {
    const graph: FlowGraph = {
      nodes: [
        { id: 'in', type: 'input', name: 'In' },
        { id: 'a', type: 'assignment', name: 'A', config: { assignments: [] } }
      ],
      edges: [
        { from: 'in', to: 'a' },
        { from: 'a', to: 'in' }
      ]
    };
    const run = runFlow(state.flows[0], graph, {});
    expect(run.status).toBe('failed');
    expect(run.error).toContain('step bound');
  });
});

describe('flow metrics by_variant', () => {
  it('does not count a suspended decision as a variant failure', () => {
    const flow = state.flows.find((f) => f.slug === 'credit-decision');
    if (!flow) throw new Error('seed flow missing');
    const read = () =>
      (
        handleDemo('GET', `/v1/flows/${flow.slug}/metrics`, params(), {}).body as {
          by_variant: Record<string, { started: number; completed: number; failed: number }>;
        }
      ).by_variant.champion;
    const before = read();
    state.decisions.unshift({
      decision_id: 'dec_suspended_metrics',
      flow_id: flow.flow_id,
      slug: flow.slug,
      version: flow.latest,
      environment: 'sandbox',
      variant: 'champion',
      status: 'suspended',
      started_at: new Date().toISOString()
    } as Decision);
    const after = read();
    expect(after.started).toBe(before.started + 1);
    expect(after.completed).toBe(before.completed);
    expect(after.failed).toBe(before.failed);
  });
});

describe('define-agent duplicate name', () => {
  it('400s with a clear message naming the agent', () => {
    const name = state.agents[0].name;
    const res = handleDemo('POST', '/v1/agents', params(), { name });
    expect(res.status).toBe(400);
    expect((res.body as { error: string }).error).toBe(`an agent named "${name}" already exists`);
  });
});
