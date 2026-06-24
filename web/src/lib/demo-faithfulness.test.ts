// SPDX-License-Identifier: AGPL-3.0-or-later
// Guards the demo-faithfulness behaviors: the agent reply is a plausible response
// (never the "stub: <prompt>" echo), a preview decide records nothing, and the
// admin-only surfaces gate on the switched user's role (matching the real backend).

import { describe, it, expect } from 'vitest';
import { agentReply } from './demo/agent';
import { handleDemo } from './demo/router';
import { setDemoUser, state, USERS, psi, modelDrift } from './demo/store';

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

  it('computes a real PSI for a model with a baseline (not a constant)', () => {
    const d = modelDrift('credit_pd');
    expect(d).toBeDefined();
    expect(typeof d?.psi).toBe('number');
    expect(modelDrift('no-such-model')).toBeUndefined();
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
