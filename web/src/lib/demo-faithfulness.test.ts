// SPDX-License-Identifier: AGPL-3.0-or-later
// Guards the demo-faithfulness behaviors: the agent reply is a plausible response
// (never the "stub: <prompt>" echo), a preview decide records nothing, and the
// admin-only surfaces gate on the switched user's role (matching the real backend).

import { describe, it, expect } from 'vitest';
import { agentReply } from './demo/agent';
import { evaluateModel } from './demo/engine';
import type { Model } from './api';
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
    expect((res.body as { disposition: string }).disposition).toBe('approve');
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
