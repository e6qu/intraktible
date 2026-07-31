// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, vi } from 'vitest';
import {
  getStats,
  sayHello,
  listFlows,
  createFlow,
  updateFlow,
  decide,
  publishVersion,
  exportFlow,
  exportAuthoringDraft,
  importFlow,
  importFlowBundle,
  createDraft,
  assessComponentCompatibility,
  createComponentUpgradeDrafts,
  exportDecision,
  listDecisions,
  listDecisionsPage,
  getDecisionExecutionSummary,
  getDecision,
  ApiError,
  getFlowMetrics,
  flowCoverage,
  backtestFlow,
  whatif,
  deployVersion,
  requestDeployment,
  approveDeployment,
  rejectDeployment,
  requestPolicyApproval,
  decidePolicyApproval,
  getShadow,
  setShadow,
  listAudit,
  auditQuery,
  auditExportUrl,
  listApiKeys,
  createApiKey,
  rotateApiKey,
  revokeApiKey,
  listConnectors,
  defineConnector,
  fetchConnector,
  listConnectorFetches,
  listFeatures,
  defineFeature,
  listEntities,
  recordEntity,
  listEntityEvents,
  recordEntityEvent,
  listCases,
  getCaseSummary,
  requestReview,
  sweepSLA,
  assignCase,
  setCaseStatus,
  listAgents,
  defineAgent,
  runAgent,
  startAgentRun,
  cancelAgentRun,
  retryAgentRun,
  escalateRun,
  getRunSummary,
  listAgentTemplates,
  createAgentRelease,
  runAgentCampaign,
  listAgentCampaigns,
  adjudicateAgentCampaignTrial,
  compareAgentCampaigns,
  downloadAgentCampaign,
  requestAgentReleaseReview,
  deployAgentRelease,
  activateAgentDeployment,
  pauseAgentDeployment,
  resumeAgentDeployment,
  rollbackAgentDeployment,
  retireAgentRelease,
  listAgentSafetyIncidents,
  openAgentSafetyIncident,
  resolveAgentSafetyIncident,
  listAgentToolApprovals,
  decideAgentToolApproval,
  getAgentGovernanceAnalytics,
  requestCaseAgentAssist,
  retryAgentAssist,
  cancelAgentAssist,
  recordAgentAssistAction,
  login,
  logout,
  currentUser,
  listSsoProviders,
  listSamlProviders,
  getErasureStatus,
  holdErasureSubject,
  releaseErasureSubject,
  eraseSubject,
  setRetentionPolicy,
  runRetentionSweep,
  issueAdverseAction,
  issuedAdverseActionNotice,
  recordContest,
  recordReconsideration,
  getModelPerformance,
  recordModelOutcome,
  createExperiment,
  transitionExperiment,
  recordOutcome,
  createPopulationJob
} from './api';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' }
  });
}

function fetcherReturning(status: number, body: unknown) {
  return vi.fn(
    async (_input: RequestInfo | URL, _init?: RequestInit): Promise<Response> =>
      jsonResponse(status, body)
  );
}

function textFetcher(status: number, body: string) {
  return vi.fn(
    async (_input: RequestInfo | URL, _init?: RequestInit): Promise<Response> =>
      new Response(body, { status, headers: { 'Content-Type': 'text/plain' } })
  );
}

describe('data governance', () => {
  it('encodes subject keys and sends each legal-hold and erasure mutation', async () => {
    const statusFetch = fetcherReturning(200, {
      subject: 'customer/a b',
      erased: false,
      held: false
    });
    await expect(getErasureStatus('k', 'customer/a b', statusFetch)).resolves.toMatchObject({
      erased: false,
      held: false
    });
    expect(statusFetch.mock.calls[0][0]).toBe('/v1/erasure/subjects/customer%2Fa%20b');

    const holdFetch = fetcherReturning(200, { held: true });
    await holdErasureSubject('k', 'customer/a b', 'dispute', holdFetch);
    expect(holdFetch.mock.calls[0][0]).toBe('/v1/erasure/subjects/customer%2Fa%20b/hold');
    expect(holdFetch.mock.calls[0][1]).toMatchObject({ method: 'POST' });
    expect(JSON.parse(holdFetch.mock.calls[0][1]?.body as string)).toEqual({
      reason: 'dispute'
    });

    const releaseFetch = fetcherReturning(200, { held: false });
    await releaseErasureSubject('k', 'customer/a b', releaseFetch);
    expect(releaseFetch.mock.calls[0][0]).toBe('/v1/erasure/subjects/customer%2Fa%20b/release');

    const eraseFetch = fetcherReturning(200, { erased: true });
    await eraseSubject('k', 'customer/a b', eraseFetch);
    expect(eraseFetch.mock.calls[0][0]).toBe('/v1/erasure/subjects/customer%2Fa%20b');
    expect(eraseFetch.mock.calls[0][1]?.method).toBe('POST');
  });

  it('stores scheduler policy and returns every manual-sweep outcome', async () => {
    const policyFetch = fetcherReturning(200, { retention_days: 90 });
    await expect(setRetentionPolicy('k', 90, policyFetch)).resolves.toEqual({
      retention_days: 90
    });
    expect(JSON.parse(policyFetch.mock.calls[0][1]?.body as string)).toEqual({
      retention_days: 90
    });

    const sweepFetch = fetcherReturning(200, {
      erased: 2,
      held: 1,
      statutory_retained: 3,
      max_age_days: 90
    });
    await expect(runRetentionSweep('k', 90, sweepFetch)).resolves.toMatchObject({
      erased: 2,
      held: 1,
      statutory_retained: 3
    });
    expect(sweepFetch.mock.calls[0][0]).toBe('/v1/erasure/retention?max_age_days=90');
    expect(sweepFetch.mock.calls[0][1]?.method).toBe('POST');
  });

  it('surfaces a retention conflict instead of reporting erasure success', async () => {
    const fetcher = fetcherReturning(409, { error: 'erasure: retained until 2028-01-01' });
    await expect(eraseSubject('k', 'customer/held', fetcher)).rejects.toMatchObject({
      status: 409,
      message: 'erasure: retained until 2028-01-01'
    });
  });
});

describe('regulatory decisions', () => {
  it('downloads the exact issued adverse-action artifact from its immutable route', async () => {
    const fetcher = textFetcher(200, '# Issued notice\n');
    await expect(issuedAdverseActionNotice('k', 'decision/1', fetcher)).resolves.toBe(
      '# Issued notice\n'
    );
    expect(fetcher.mock.calls[0][0]).toBe('/v1/decisions/decision%2F1/adverse-action/issued');
  });

  it('returns durable event acknowledgements for issuance, contest, and reconsideration', async () => {
    const issueFetch = fetcherReturning(200, { event_id: 'issued-1', seq: 41 });
    await expect(
      issueAdverseAction(
        'k',
        'decision/1',
        { method: 'email', based_on_consumer_report: false },
        issueFetch
      )
    ).resolves.toEqual({ event_id: 'issued-1', seq: 41 });
    expect(issueFetch.mock.calls[0][0]).toBe('/v1/decisions/decision%2F1/adverse-action/issue');

    const contestFetch = fetcherReturning(200, { event_id: 'contest-1', seq: 42 });
    await expect(
      recordContest(
        'k',
        'decision/1',
        { channel: 'online_portal', note: 'Please review' },
        contestFetch
      )
    ).resolves.toEqual({ event_id: 'contest-1', seq: 42 });

    const reviewFetch = fetcherReturning(200, { event_id: 'review-1', seq: 43 });
    await expect(
      recordReconsideration(
        'k',
        'decision/1',
        { basis: 'applicant_contest', outcome: 'overturned', rationale: 'Verified correction' },
        reviewFetch
      )
    ).resolves.toEqual({ event_id: 'review-1', seq: 43 });
  });
});

describe('export', () => {
  it('exportFlow requests the format and returns the raw diagram text', async () => {
    const fetcher = textFetcher(200, 'flowchart TD\n');
    const out = await exportFlow('k', 'f1', 'bpmn', fetcher);
    expect(out).toBe('flowchart TD\n');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/flows/f1/export?format=bpmn');
    expect(init?.headers).toMatchObject({ 'X-Api-Key': 'k' });
  });

  it('exportDecision fetches the decision trace', async () => {
    const fetcher = textFetcher(200, 'sequenceDiagram\n');
    const out = await exportDecision('k', 'd9', 'mermaid', fetcher);
    expect(out).toBe('sequenceDiagram\n');
    expect(fetcher.mock.calls[0][0]).toBe('/v1/decisions/d9/export?format=mermaid');
  });

  it('exportDecision requests the chosen format', async () => {
    const fetcher = textFetcher(200, 'digraph run {}');
    await exportDecision('k', 'd9', 'dot', fetcher);
    expect(fetcher.mock.calls[0][0]).toBe('/v1/decisions/d9/export?format=dot');
  });

  it('throws loudly on a non-2xx export', async () => {
    await expect(exportFlow('k', 'f1', 'mermaid', textFetcher(404, ''))).rejects.toThrow(/404/);
  });

  it('exports the durable draft through the canonical authoring route', async () => {
    const fetcher = textFetcher(200, '{"format_version":"intraktible.authoring/v1"}');
    const out = await exportAuthoringDraft('k', 'draft/1', fetcher);
    expect(out).toContain('intraktible.authoring/v1');
    expect(fetcher.mock.calls[0][0]).toBe('/v1/authoring/drafts/draft%2F1/export');
  });

  it('importFlow posts the document and returns the result', async () => {
    const fetcher = fetcherReturning(201, {
      flow_id: 'f9',
      draft_id: 'd1',
      revision: 1,
      created: false,
      event_id: 'evt',
      seq: 12
    });
    const doc = { slug: 'iac', name: 'IaC', graph: { nodes: [], edges: [] } };
    const out = await importFlow('k', doc, fetcher, 'commit-1');
    expect(out).toMatchObject({ flow_id: 'f9', draft_id: 'd1', revision: 1 });
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/authoring/import');
    expect(init?.method).toBe('POST');
    expect(init?.headers).toMatchObject({ 'Idempotency-Key': 'commit-1' });
    expect(JSON.parse(init?.body as string)).toEqual({
      ...doc,
      format_version: 'intraktible.authoring/v1',
      kind: 'flow'
    });
  });

  it('drops target metadata from a recognized legacy flow export', async () => {
    const fetcher = fetcherReturning(201, {
      flow_id: 'f9',
      draft_id: 'd1',
      revision: 1,
      created: true,
      migration_report: { rewrites: [] }
    });
    await importFlow(
      'k',
      {
        slug: 'portable',
        name: 'Portable',
        version: 7,
        etag: 'target-etag',
        graph: { nodes: [], edges: [] }
      },
      fetcher,
      'legacy-1'
    );
    expect(JSON.parse(fetcher.mock.calls[0][1]?.body as string)).toEqual({
      format_version: 'intraktible.authoring/v1',
      kind: 'flow',
      slug: 'portable',
      name: 'Portable',
      graph: { nodes: [], edges: [] }
    });
  });

  it('preserves unknown fields on marked canonical source for strict server rejection', async () => {
    const fetcher = fetcherReturning(400, { error: 'unknown field "versoin"' });
    await expect(
      importFlow(
        'k',
        {
          format_version: 'intraktible.authoring/v1',
          kind: 'flow',
          slug: 'strict',
          name: 'Strict',
          graph: { nodes: [], edges: [] },
          versoin: 7
        },
        fetcher,
        'strict-1'
      )
    ).rejects.toThrow(/unknown field/);
    expect(JSON.parse(fetcher.mock.calls[0][1]?.body as string).versoin).toBe(7);
  });

  it('importFlowBundle posts the versioned bundle and returns its drafts', async () => {
    const fetcher = fetcherReturning(201, {
      imports: [
        { flow_id: 'f1', draft_id: 'd1', revision: 1, created: true },
        { flow_id: 'f2', draft_id: 'd2', revision: 1, created: true }
      ],
      seq: 14
    });
    const flows = [
      { slug: 'a', name: 'A', graph: { nodes: [], edges: [] } },
      { slug: 'b', name: 'B', graph: { nodes: [], edges: [] } }
    ];
    const out = await importFlowBundle('k', { flows }, fetcher, 'bundle-1');
    expect(out.imports).toHaveLength(2);
    expect(out.seq).toBe(14);
    expect(fetcher.mock.calls[0][0]).toBe('/v1/authoring/import-bundle');
    expect(JSON.parse(fetcher.mock.calls[0][1]?.body as string)).toEqual({
      format_version: 'intraktible.authoring/v1',
      kind: 'bundle',
      flows: flows.map((flow) => ({
        ...flow,
        format_version: 'intraktible.authoring/v1',
        kind: 'flow'
      }))
    });
  });

  it('importFlow rejects a non-object before issuing a request', async () => {
    const fetcher = fetcherReturning(201, {});
    await expect(importFlow('k', '{"slug":"iac"}', fetcher)).rejects.toThrow(/JSON object/);
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('preserves explicit canonical versions so the server rejects unsupported semantics', async () => {
    const fetcher = fetcherReturning(400, { error: 'unsupported canonical format' });
    await expect(
      importFlow(
        'k',
        {
          format_version: 'intraktible.authoring/v2',
          kind: 'flow',
          slug: 'future',
          graph: { nodes: [], edges: [] }
        },
        fetcher
      )
    ).rejects.toThrow(/unsupported canonical format/);
    expect(JSON.parse(fetcher.mock.calls[0][1]?.body as string).format_version).toBe(
      'intraktible.authoring/v2'
    );
  });

  it('uses durable retry identities for authoring create and upgrade mutations', async () => {
    const draftFetcher = fetcherReturning(201, {
      draft_id: 'd1',
      revision: 1,
      event_id: 'e1',
      seq: 2
    });
    await createDraft(
      'k',
      {
        flow_id: 'f1',
        base_version: 1,
        title: 'Draft',
        graph: { nodes: [], edges: [] }
      },
      draftFetcher,
      'draft-key'
    );
    expect(draftFetcher.mock.calls[0][1]?.headers).toMatchObject({
      'Idempotency-Key': 'draft-key'
    });

    const upgradeFetcher = fetcherReturning(201, {
      drafts: [{ flow_id: 'f1', draft_id: 'd2', base_version: 1, revision: 1 }],
      seq: 3
    });
    const upgraded = await createComponentUpgradeDrafts(
      'k',
      'component/1',
      { from_version: 1, to_version: 2, flow_ids: ['f1'] },
      upgradeFetcher,
      'upgrade-key'
    );
    expect(upgraded.drafts[0].draft_id).toBe('d2');
    expect(upgradeFetcher.mock.calls[0][0]).toBe(
      '/v1/authoring/components/component%2F1/upgrade-drafts'
    );
    expect(upgradeFetcher.mock.calls[0][1]?.headers).toMatchObject({
      'Idempotency-Key': 'upgrade-key'
    });
  });

  it('reads server-owned component compatibility evidence', async () => {
    const fetcher = fetcherReturning(200, {
      component_id: 'c1',
      report: { from_version: 1, to_version: 2, status: 'compatible' },
      consumers: [],
      upgradeable: true
    });
    const assessment = await assessComponentCompatibility('k', 'c1', 1, 2, fetcher);
    expect(assessment.upgradeable).toBe(true);
    expect(fetcher.mock.calls[0][0]).toBe(
      '/v1/authoring/components/c1/compatibility?from_version=1&to_version=2'
    );
  });
});

describe('decisions + analytics', () => {
  it('listDecisions unwraps the decisions array', async () => {
    const fetcher = fetcherReturning(200, {
      decisions: [{ decision_id: 'd1', slug: 's', status: 'completed' }]
    });
    const ds = await listDecisions('k', fetcher);
    expect(ds).toHaveLength(1);
    expect(ds[0].decision_id).toBe('d1');
    expect(fetcher.mock.calls[0][0]).toBe('/v1/decisions');
  });

  it('getDecision fetches one decision by id', async () => {
    const fetcher = fetcherReturning(200, { decision_id: 'd9', status: 'failed' });
    const d = await getDecision('k', 'd9', fetcher);
    expect(d.status).toBe('failed');
    expect(fetcher.mock.calls[0][0]).toBe('/v1/decisions/d9');
  });

  it('gets the durable decision execution summary', async () => {
    const fetcher = fetcherReturning(200, {
      total: 3,
      running: 1,
      retrying: 1,
      suspended: 0,
      completed: 0,
      failed: 0,
      abandoned: 1,
      recovery_attempts: 4,
      attention: [{ decision_id: 'd1', slug: 'risk', status: 'retrying' }]
    });
    const summary = await getDecisionExecutionSummary('k', fetcher);
    expect(summary.recovery_attempts).toBe(4);
    expect(summary.attention[0].decision_id).toBe('d1');
    expect(fetcher.mock.calls[0][0]).toBe('/v1/decisions/summary');
  });

  it('listDecisionsPage encodes exact scalar metadata filters as a deep object', async () => {
    const fetcher = fetcherReturning(200, {
      decisions: [],
      total: 0,
      limit: 25,
      offset: 0
    });
    await listDecisionsPage(
      'k',
      { metadata: { channel: 'branch', priority: 2, reviewed: true }, limit: 25 },
      fetcher
    );
    const url = new URL(String(fetcher.mock.calls[0][0]), 'http://api');
    expect(url.searchParams.get('metadata[channel]')).toBe('branch');
    expect(url.searchParams.get('metadata[priority]')).toBe('2');
    expect(url.searchParams.get('metadata[reviewed]')).toBe('true');
    expect(url.searchParams.get('metadata')).toBeNull();
  });

  it('getFlowMetrics hits the flow metrics endpoint', async () => {
    const fetcher = fetcherReturning(200, {
      total: 5,
      completed: 4,
      failed: 1,
      avg_duration_ms: 12
    });
    const m = await getFlowMetrics('k', 'f1', fetcher);
    expect(m.total).toBe(5);
    expect(fetcher.mock.calls[0][0]).toBe('/v1/flows/f1/metrics');
  });

  it('listDecisions throws loudly on a non-2xx', async () => {
    await expect(listDecisions('k', fetcherReturning(401, {}))).rejects.toThrow(/401/);
  });

  it('getDecision throws an ApiError carrying the status so a caller can single out a 404', async () => {
    const err = await getDecision('k', 'gone', fetcherReturning(404, {})).catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(404);
  });

  it('ApiError.status distinguishes a real 404 from other failures', async () => {
    const notFound = await getDecision('k', 'gone', fetcherReturning(404, {})).catch((e) => e);
    const serverError = await getDecision(
      'k',
      'boom',
      fetcherReturning(500, { error: 'kaboom' })
    ).catch((e) => e);
    expect((notFound as ApiError).status).toBe(404);
    expect((serverError as ApiError).status).toBe(500);
    // The backend's message is preserved verbatim (not masked as a generic not-found).
    expect((serverError as ApiError).message).toBe('kaboom');
  });
});

describe('backtest', () => {
  it('posts the dataset and compare version, returns the report', async () => {
    const fetcher = fetcherReturning(200, {
      summary: { total: 2, compare: true, baseline_completed: 2, baseline_failed: 0, changed: 1 },
      records: [
        {
          index: 1,
          baseline: { status: 'completed' },
          candidate: { status: 'completed' },
          changed: true
        }
      ]
    });
    const rep = await backtestFlow(
      'k',
      'f1',
      { compare_version: 1, dataset: [{ score: 720 }, { score: 540 }] },
      fetcher
    );
    expect(rep.summary.changed).toBe(1);
    expect(rep.records[0].changed).toBe(true);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/flows/f1/backtest');
    expect(init?.method).toBe('POST');
    expect(JSON.parse(String(init?.body))).toMatchObject({
      compare_version: 1,
      dataset: [{ score: 720 }, { score: 540 }]
    });
  });

  it('surfaces the server error message on a non-2xx', async () => {
    await expect(
      backtestFlow(
        'k',
        'f1',
        { dataset: [] },
        fetcherReturning(400, { error: 'dataset is required' })
      )
    ).rejects.toThrow(/dataset is required/);
  });

  it('whatif posts the sweep and returns the report', async () => {
    const fetcher = fetcherReturning(200, {
      field: 'score',
      transitions: 1,
      points: [
        { value: 1, status: 'completed', output: { decision: 'B' }, changed: false },
        { value: 9, status: 'completed', output: { decision: 'A' }, changed: true }
      ]
    });
    const rep = await whatif('k', 'f1', { base: {}, field: 'score', values: [1, 9] }, fetcher);
    expect(rep.transitions).toBe(1);
    expect(rep.points[1].changed).toBe(true);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/flows/f1/whatif');
    expect(JSON.parse(String(init?.body))).toEqual({ base: {}, field: 'score', values: [1, 9] });
  });
});

describe('deployment & maker-checker', () => {
  it('deployVersion posts to the deployments endpoint', async () => {
    const fetcher = fetcherReturning(201, { environment: 'sandbox', version: 2 });
    await deployVersion('k', 'f1', { environment: 'sandbox', version: 2 }, fetcher);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/flows/f1/deployments');
    expect(init?.method).toBe('POST');
    expect(JSON.parse(String(init?.body))).toMatchObject({ environment: 'sandbox', version: 2 });
  });

  it('requestDeployment proposes and returns the request id', async () => {
    const fetcher = fetcherReturning(201, { request_id: 'req-9', status: 'pending' });
    const r = await requestDeployment(
      'k',
      'f1',
      { environment: 'production', version: 3, challenger_version: 2, challenger_pct: 10 },
      fetcher
    );
    expect(r.request_id).toBe('req-9');
    expect(fetcher.mock.calls[0][0]).toBe('/v1/flows/f1/deployment-requests');
  });

  it('approveDeployment hits the approve endpoint', async () => {
    const fetcher = fetcherReturning(200, { status: 'approved' });
    await approveDeployment('k', 'f1', 'req-9', 'looks good', fetcher);
    expect(fetcher.mock.calls[0][0]).toBe('/v1/flows/f1/deployment-requests/req-9/approve');
  });

  it('surfaces the four-eyes self-approval error loudly', async () => {
    await expect(
      approveDeployment(
        'k',
        'f1',
        'req-9',
        '',
        fetcherReturning(400, { error: 'cannot approve your own deployment request' })
      )
    ).rejects.toThrow(/own deployment request/);
  });

  it('rejectDeployment sends the reason', async () => {
    const fetcher = fetcherReturning(200, { status: 'rejected' });
    await rejectDeployment('k', 'f1', 'req-9', 'nope', fetcher);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/flows/f1/deployment-requests/req-9/reject');
    expect(JSON.parse(String(init?.body))).toMatchObject({ reason: 'nope' });
  });
});

describe('policy maker-checker', () => {
  it('requests review of the latest immutable version', async () => {
    const fetcher = fetcherReturning(200, { request_id: 'policy-req-9' });
    await expect(requestPolicyApproval('k', 'policy/a', fetcher)).resolves.toEqual({
      request_id: 'policy-req-9'
    });
    expect(fetcher.mock.calls[0][0]).toBe('/v1/policies/policy%2Fa/approval-request');
    expect(fetcher.mock.calls[0][1]).toMatchObject({ method: 'POST' });
  });

  it('records an approval or rejection with the exact request and reason', async () => {
    const approve = fetcherReturning(200, { status: 'approved' });
    await decidePolicyApproval('k', 'policy/a', 'req-1', true, 'bands reviewed', approve);
    expect(approve.mock.calls[0][0]).toBe('/v1/policies/policy%2Fa/approve');
    expect(JSON.parse(String(approve.mock.calls[0][1]?.body))).toEqual({
      request_id: 'req-1',
      reason: 'bands reviewed'
    });

    const reject = fetcherReturning(200, { status: 'rejected' });
    await decidePolicyApproval('k', 'policy/a', 'req-2', false, 'impact too wide', reject);
    expect(reject.mock.calls[0][0]).toBe('/v1/policies/policy%2Fa/reject');
  });
});

describe('shadow', () => {
  it('getShadow returns assignments and the report, defaulting empties', async () => {
    const fetcher = fetcherReturning(200, {
      shadows: { sandbox: 2 },
      report: {
        sandbox: {
          live_version: 1,
          shadow_version: 2,
          match_basis: 'output',
          total: 10,
          matched: 7,
          diverged: 3,
          errored: 0,
          samples: [
            {
              decision_id: 'decision-1',
              live_status: 'completed',
              shadow_status: 'completed',
              changed_fields: ['decision']
            }
          ]
        }
      },
      cohorts: {
        exact: {
          environment: 'sandbox',
          experiment_id: 'exp-1',
          experiment_cohort: 2,
          experiment_arm: 'champion',
          live_version: 1,
          shadow_version: 2,
          match_basis: 'output',
          total: 10,
          matched: 7,
          diverged: 3,
          errored: 0
        }
      }
    });
    const s = await getShadow('k', 'f1', fetcher);
    expect(s.shadows.sandbox).toBe(2);
    expect(s.report.sandbox.diverged).toBe(3);
    expect(s.report.sandbox.match_basis).toBe('output');
    expect(s.report.sandbox.samples?.[0].changed_fields).toEqual(['decision']);
    expect(s.cohorts.exact.experiment_cohort).toBe(2);
    expect(fetcher.mock.calls[0][0]).toBe('/v1/flows/f1/shadow');
  });

  it('setShadow PUTs the environment and version', async () => {
    const fetcher = fetcherReturning(200, {});
    await setShadow('k', 'f1', 'sandbox', 3, fetcher);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/flows/f1/shadow');
    expect(init?.method).toBe('PUT');
    expect(JSON.parse(String(init?.body))).toEqual({ environment: 'sandbox', version: 3 });
  });
});

describe('audit', () => {
  it('builds the query string from a filter', () => {
    expect(auditQuery({})).toBe('');
    expect(auditQuery({ stream: 'flows', actor: 'ada', limit: 50 })).toBe(
      '?stream=flows&actor=ada&limit=50'
    );
  });

  it('listAudit unwraps the entries array and passes filters', async () => {
    const fetcher = fetcherReturning(200, {
      entries: [
        { seq: 2, id: 'e2', time: 't', actor: 'ada', stream: 'flows', type: 'flow.created' }
      ]
    });
    const entries = await listAudit('k', { stream: 'flows', resource: 'f1' }, fetcher);
    expect(entries).toHaveLength(1);
    expect(entries[0].actor).toBe('ada');
    expect(fetcher.mock.calls[0][0]).toBe('/v1/audit?stream=flows&resource=f1');
  });

  it('surfaces the 403 admin restriction loudly', async () => {
    await expect(
      listAudit('k', {}, fetcherReturning(403, { error: 'requires at least the "admin" role' }))
    ).rejects.toThrow(/admin/);
  });

  it('auditExportUrl appends format=csv', () => {
    expect(auditExportUrl({})).toBe('/v1/audit?format=csv');
    expect(auditExportUrl({ stream: 'cases' })).toBe('/v1/audit?stream=cases&format=csv');
  });
});

describe('managed api keys', () => {
  it('listApiKeys unwraps the api_keys array', async () => {
    const fetcher = fetcherReturning(200, {
      api_keys: [
        {
          id: 'k1',
          name: 'CI',
          identity: { org: 'o', workspace: 'w', actor: 'ci' },
          scope: 'sandbox',
          role: 'editor',
          created_at: 't'
        }
      ]
    });
    const keys = await listApiKeys('k', fetcher);
    expect(keys).toHaveLength(1);
    expect(keys[0].name).toBe('CI');
    expect(fetcher.mock.calls[0][0]).toBe('/v1/api-keys');
  });

  it('createApiKey posts the request and returns the one-time secret', async () => {
    const fetcher = fetcherReturning(201, {
      api_key: { id: 'k2', name: 'bot', role: 'viewer', scope: 'sandbox' },
      secret: 'itk_abc123'
    });
    const out = await createApiKey('k', { name: 'bot', actor: 'a', role: 'viewer' }, fetcher);
    expect(out.secret).toBe('itk_abc123');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/api-keys');
    expect(init?.method).toBe('POST');
    expect(JSON.parse(init?.body as string)).toMatchObject({ name: 'bot', role: 'viewer' });
  });

  it('rotateApiKey posts the grace window and returns the new secret', async () => {
    const fetcher = fetcherReturning(200, {
      api_key: { id: 'k2', name: 'bot', prev_hash_expires_at: 't1' },
      secret: 'itk_rotated'
    });
    const out = await rotateApiKey('k', 'k2', 3600, fetcher);
    expect(out.secret).toBe('itk_rotated');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/api-keys/k2/rotate');
    expect(init?.method).toBe('POST');
    expect(JSON.parse(init?.body as string)).toEqual({ grace_seconds: 3600 });
  });

  it('revokeApiKey deletes by id and unwraps the key', async () => {
    const fetcher = fetcherReturning(200, { api_key: { id: 'k3', revoked_at: 't' } });
    const k = await revokeApiKey('k', 'k3', fetcher);
    expect(k.revoked_at).toBe('t');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/api-keys/k3');
    expect(init?.method).toBe('DELETE');
  });

  it('surfaces the 403 admin restriction loudly', async () => {
    await expect(
      listApiKeys('k', fetcherReturning(403, { error: 'requires at least the "admin" role' }))
    ).rejects.toThrow(/admin/);
  });
});

describe('context layer', () => {
  it('listConnectors unwraps the connectors array', async () => {
    const fetcher = fetcherReturning(200, {
      connectors: [{ name: 'bureau', type: 'mock_bureau' }]
    });
    const cs = await listConnectors('k', fetcher);
    expect(cs[0].type).toBe('mock_bureau');
    expect(fetcher.mock.calls[0][0]).toBe('/v1/context/connectors');
  });

  it('defineConnector posts name/type/config', async () => {
    const fetcher = fetcherReturning(201, {});
    await defineConnector('k', { name: 'b', type: 'http', config: { url: 'https://x' } }, fetcher);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/context/connectors');
    expect(JSON.parse(String(init?.body))).toMatchObject({ name: 'b', type: 'http' });
  });

  it('tests a connector and reads its recorded fetches', async () => {
    const fetch = fetcherReturning(200, {
      fetch_id: 'fx-1',
      response: { risk_score: 42 }
    });
    const result = await fetchConnector('k', 'bureau one', { subject: 'a' }, fetch);
    expect(result.response).toEqual({ risk_score: 42 });
    expect(fetch.mock.calls[0][0]).toBe('/v1/context/connectors/bureau%20one/fetch');
    expect(JSON.parse(String(fetch.mock.calls[0][1]?.body))).toEqual({
      params: { subject: 'a' }
    });

    const list = fetcherReturning(200, {
      fetches: [{ fetch_id: 'fx-1', connector: 'bureau one', response: { risk_score: 42 } }]
    });
    const history = await listConnectorFetches('k', 'bureau one', list);
    expect(history[0].fetch_id).toBe('fx-1');
    expect(list.mock.calls[0][0]).toBe('/v1/context/connectors/bureau%20one/fetches');
  });

  it('listFeatures unwraps the features array', async () => {
    const fetcher = fetcherReturning(200, {
      features: [{ name: 'txn_24h', entity_type: 'cust', aggregation: 'count', window_hours: 24 }]
    });
    const fs = await listFeatures('k', fetcher);
    expect(fs[0].name).toBe('txn_24h');
  });

  it('defineFeature posts the spec', async () => {
    const fetcher = fetcherReturning(201, {});
    await defineFeature(
      'k',
      {
        name: 'f',
        entity_type: 'c',
        event_name: 'txn',
        aggregation: 'sum',
        field: 'amt',
        window_hours: 24
      },
      fetcher
    );
    expect(JSON.parse(String(fetcher.mock.calls[0][1]?.body))).toMatchObject({
      aggregation: 'sum',
      field: 'amt'
    });
  });

  it('listEntities passes a type filter', async () => {
    const fetcher = fetcherReturning(200, { entities: [] });
    await listEntities('k', 'customer', fetcher);
    expect(fetcher.mock.calls[0][0]).toBe('/v1/context/entities?type=customer');
  });

  it('records an entity and an event', async () => {
    const entityFetch = fetcherReturning(202, {});
    await recordEntity(
      'k',
      { entity_type: 'customer', entity_id: 'c1', attributes: { tier: 'gold' } },
      entityFetch
    );
    expect(entityFetch.mock.calls[0][0]).toBe('/v1/context/entities');
    expect(JSON.parse(String(entityFetch.mock.calls[0][1]?.body))).toMatchObject({
      entity_type: 'customer',
      entity_id: 'c1',
      attributes: { tier: 'gold' }
    });

    const eventFetch = fetcherReturning(202, {});
    await recordEntityEvent(
      'k',
      {
        entity_type: 'customer',
        entity_id: 'c1',
        event_name: 'transaction',
        data: { amount: 12 },
        occurred_at: '2026-01-01T00:00:00Z'
      },
      eventFetch
    );
    expect(eventFetch.mock.calls[0][0]).toBe('/v1/context/events');
    expect(JSON.parse(String(eventFetch.mock.calls[0][1]?.body))).toMatchObject({
      event_name: 'transaction',
      data: { amount: 12 }
    });
  });

  it('listEntityEvents hits the per-entity events endpoint', async () => {
    const fetcher = fetcherReturning(200, { events: [{ event_name: 'txn', seq: 1 }] });
    const evs = await listEntityEvents('k', 'customer', 'c1', fetcher);
    expect(evs).toHaveLength(1);
    expect(fetcher.mock.calls[0][0]).toBe('/v1/context/entities/customer/c1/events');
  });
});

describe('getStats', () => {
  it('sends the api key and parses the stats body', async () => {
    const fetcher = fetcherReturning(200, { count: 2, last_name: 'ada' });
    const stats = await getStats('k', fetcher);

    expect(stats.count).toBe(2);
    expect(stats.last_name).toBe('ada');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/hello/stats');
    expect(init?.headers).toMatchObject({ 'X-Api-Key': 'k' });
  });

  it('throws loudly on a non-2xx response', async () => {
    await expect(getStats('k', fetcherReturning(401, {}))).rejects.toThrow(/401/);
  });
});

describe('sayHello', () => {
  it('posts the name with the right headers', async () => {
    const fetcher = fetcherReturning(202, { event_id: 'e1', seq: 1 });
    const result = await sayHello('k', 'grace', fetcher);

    expect(result.seq).toBe(1);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/hello');
    expect(init?.method).toBe('POST');
    expect(init?.body).toBe(JSON.stringify({ name: 'grace' }));
    expect(init?.headers).toMatchObject({ 'X-Api-Key': 'k', 'Content-Type': 'application/json' });
  });

  it('throws loudly on a non-2xx response', async () => {
    await expect(sayHello('k', 'x', fetcherReturning(400, {}))).rejects.toThrow(/400/);
  });
});

describe('flows', () => {
  it('unwraps the flows array', async () => {
    const fetcher = fetcherReturning(200, {
      flows: [{ flow_id: 'f1', slug: 's', name: 'N', latest: 1 }]
    });
    const flows = await listFlows('k', fetcher);
    expect(flows).toHaveLength(1);
    expect(flows[0].slug).toBe('s');
  });

  it('createFlow posts slug and name, omitting a blank description', async () => {
    const fetcher = fetcherReturning(201, { flow_id: 'f1' });
    const res = await createFlow('k', 'my-flow', 'My Flow', '', fetcher);
    expect(res.flow_id).toBe('f1');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/flows');
    expect(init?.method).toBe('POST');
    expect(init?.body).toBe(JSON.stringify({ slug: 'my-flow', name: 'My Flow' }));
  });

  it('createFlow includes a non-blank description', async () => {
    const fetcher = fetcherReturning(201, { flow_id: 'f1' });
    await createFlow('k', 'my-flow', 'My Flow', ' Scores loans. ', fetcher);
    const [, init] = fetcher.mock.calls[0];
    expect(init?.body).toBe(
      JSON.stringify({ slug: 'my-flow', name: 'My Flow', description: 'Scores loans.' })
    );
  });

  it('updateFlow PATCHes the description', async () => {
    const fetcher = fetcherReturning(200, {});
    await updateFlow('k', 'f1', { description: 'Now with drift checks.' }, fetcher);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/flows/f1');
    expect(init?.method).toBe('PATCH');
    expect(init?.body).toBe(JSON.stringify({ description: 'Now with drift checks.' }));
  });

  it('updateFlow surfaces the backend error loudly', async () => {
    const fetcher = fetcherReturning(404, { error: 'flow not found' });
    await expect(updateFlow('k', 'nope', { description: 'x' }, fetcher)).rejects.toThrow(
      /flow not found/
    );
  });

  it('publishVersion posts the graph and returns the version', async () => {
    const fetcher = fetcherReturning(201, { version: 2, etag: 'abc' });
    const res = await publishVersion('k', 'f1', { nodes: [], edges: [] }, undefined, fetcher);
    expect(res.version).toBe(2);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/flows/f1/versions');
    expect(init?.body).toBe(JSON.stringify({ graph: { nodes: [], edges: [] } }));
  });

  it('publishVersion includes input_schema when given', async () => {
    const fetcher = fetcherReturning(201, { version: 3, etag: 'def' });
    await publishVersion('k', 'f1', { nodes: [], edges: [] }, { type: 'object' }, fetcher);
    const [, init] = fetcher.mock.calls[0];
    expect(init?.body).toBe(
      JSON.stringify({ graph: { nodes: [], edges: [] }, input_schema: { type: 'object' } })
    );
  });

  it('publishVersion surfaces the backend validation error', async () => {
    const fetcher = fetcherReturning(400, { error: 'graph needs exactly one input node' });
    await expect(
      publishVersion('k', 'f1', { nodes: [], edges: [] }, undefined, fetcher)
    ).rejects.toThrow(/exactly one input/);
  });

  it('decide targets the slug/env path', async () => {
    const fetcher = fetcherReturning(200, {
      decision_id: 'd1',
      status: 'completed',
      data: { x: 1 }
    });
    const res = await decide('k', 'scoring', 'production', { fico: 700 }, undefined, fetcher);
    expect(res.status).toBe('completed');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/flows/scoring/production/decide');
    expect(init?.body).toBe(JSON.stringify({ data: { fico: 700 } }));
  });

  it('decide includes the entity ref when provided', async () => {
    const fetcher = fetcherReturning(200, { decision_id: 'd1', status: 'completed', data: {} });
    await decide('k', 'risk', 'production', {}, { type: 'customer', id: 'c1' }, fetcher);
    const [, init] = fetcher.mock.calls[0];
    expect(init?.body).toBe(JSON.stringify({ data: {}, entity_type: 'customer', entity_id: 'c1' }));
  });

  it('decide carries durable invocation tracking, controls, and the idempotency header', async () => {
    const fetcher = fetcherReturning(200, { decision_id: 'd1', status: 'completed', data: {} });
    await decide('k', 'risk', 'sandbox', { amount: 9 }, undefined, fetcher, false, {
      idempotencyKey: 'retry-9',
      businessReference: 'application-9',
      correlationId: 'trace-9',
      metadata: { channel: 'branch' },
      control: { timeout_ms: 900 }
    });
    const [, init] = fetcher.mock.calls[0];
    expect(init?.headers).toMatchObject({ 'Idempotency-Key': 'retry-9' });
    expect(JSON.parse(init?.body as string)).toEqual({
      data: { amount: 9 },
      business_reference: 'application-9',
      correlation_id: 'trace-9',
      metadata: { channel: 'branch' },
      control: { timeout_ms: 900 }
    });
  });

  it('decide carries preview-only provider mocks without durable tracking', async () => {
    const fetcher = fetcherReturning(200, { decision_id: '', status: 'completed', data: {} });
    await decide('k', 'risk', 'sandbox', { amount: 9 }, undefined, fetcher, true, {
      mockData: { connect: { bureau: { score: 700 } } }
    });
    const [, init] = fetcher.mock.calls[0];
    expect(JSON.parse(init?.body as string)).toEqual({
      data: { amount: 9 },
      preview: true,
      mock_data: { connect: { bureau: { score: 700 } } }
    });
  });
});

describe('cases', () => {
  it('listCases applies filters as query params and unwraps the array', async () => {
    const fetcher = fetcherReturning(200, { cases: [{ case_id: 'c1', status: 'needs_review' }] });
    const cs = await listCases('k', { status: 'needs_review', type: 'aml' }, fetcher);
    expect(cs).toHaveLength(1);
    const [url] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/cases?status=needs_review&type=aml');
  });

  it('requestReview posts the case fields', async () => {
    const fetcher = fetcherReturning(201, { case_id: 'c1' });
    const res = await requestReview(
      'k',
      { company_name: 'Acme', case_type: 'aml', sla_days: 5 },
      fetcher
    );
    expect(res.case_id).toBe('c1');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/cases');
    expect(init?.body).toBe(
      JSON.stringify({ company_name: 'Acme', case_type: 'aml', sla_days: 5 })
    );
  });

  it('assignCase claims a case without taking it from anyone', async () => {
    const fetcher = fetcherReturning(202, {});
    await assignCase('k', 'c1', 'adam', false, fetcher);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/cases/c1/assign');
    expect(init?.body).toBe(JSON.stringify({ assignee: 'adam', reassign: false }));
  });

  it('assignCase asks to take over only when told to', async () => {
    const fetcher = fetcherReturning(202, {});
    await assignCase('k', 'c1', 'adam', true, fetcher);
    const [, init] = fetcher.mock.calls[0];
    expect(init?.body).toBe(JSON.stringify({ assignee: 'adam', reassign: true }));
  });

  it('setCaseStatus surfaces the backend error', async () => {
    const fetcher = fetcherReturning(400, { error: 'unknown case' });
    await expect(setCaseStatus('k', 'ghost', 'completed', fetcher)).rejects.toThrow(/unknown case/);
  });

  it('getCaseSummary hits the summary endpoint with filters', async () => {
    const fetcher = fetcherReturning(200, {
      total: 3,
      by_status: { needs_review: 2, in_progress: 1 },
      unassigned: 1,
      due_soon: 1,
      overdue: 1
    });
    const sum = await getCaseSummary('k', { assignee: 'adam' }, fetcher);
    expect(sum.total).toBe(3);
    expect(sum.overdue).toBe(1);
    const [url] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/cases/summary?assignee=adam');
  });

  it('sweepSLA posts to the sla-sweep endpoint and returns the count', async () => {
    const fetcher = fetcherReturning(200, { breached: ['c1'], count: 1 });
    const res = await sweepSLA('k', fetcher);
    expect(res.count).toBe(1);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/cases/sla-sweep');
    expect(init?.method).toBe('POST');
  });
});

describe('agents', () => {
  it('lists governed templates and creates an immutable release spec', async () => {
    const listFetch = fetcherReturning(200, {
      templates: [{ template_id: 't 1', slug: 'case-assist', name: 'Case assist' }]
    });
    const templates = await listAgentTemplates('k', listFetch);
    expect(templates[0].template_id).toBe('t 1');

    const releaseFetch = fetcherReturning(201, { release: 2, spec_hash: 'abc', seq: 9 });
    await createAgentRelease(
      'k',
      't 1',
      {
        instructions: 'cite',
        provider: 'openai',
        model: 'gpt',
        input_schema: { type: 'object' },
        output_schema: { type: 'object' },
        tools: [],
        data_purposes: ['case_review'],
        dependencies: [],
        budget: {
          max_prompt_tokens: 100,
          max_completion_tokens: 100,
          max_tool_calls: 0,
          max_cost_usd: 0.1,
          input_cost_per_mtok: 0,
          output_cost_per_mtok: 0,
          pricing_source: 'contract',
          pricing_version: '1'
        },
        timeout_ms: 1000,
        max_attempts: 1,
        require_citations: true,
        require_human_gate: true,
        allow_remote_agent: false
      },
      releaseFetch
    );
    expect(releaseFetch.mock.calls[0][0]).toBe('/v1/agent-templates/t%201/releases');
    expect(JSON.parse(releaseFetch.mock.calls[0][1]?.body as string).spec.model).toBe('gpt');
  });

  it('runs evaluation, requests review, and deploys exact release lineage', async () => {
    const campaignFetch = fetcherReturning(201, {
      campaign: { campaign_id: 'campaign-1', blocking: false }
    });
    await runAgentCampaign('k', 'template', 3, 'safety', 2, campaignFetch);
    expect(campaignFetch.mock.calls[0][0]).toBe(
      '/v1/agent-templates/template/releases/3/campaigns'
    );
    expect(JSON.parse(campaignFetch.mock.calls[0][1]?.body as string)).toEqual({
      suite_id: 'safety',
      suite_version: 2
    });

    const listFetch = fetcherReturning(200, { campaigns: [{ campaign_id: 'campaign-1' }] });
    expect(await listAgentCampaigns('k', 'template', 3, listFetch)).toHaveLength(1);

    const adjudicationFetch = fetcherReturning(200, { event_id: 'event-1', seq: 4 });
    await adjudicateAgentCampaignTrial(
      'k',
      'campaign/1',
      'attack/1',
      2,
      true,
      'Semantically equivalent refusal',
      adjudicationFetch
    );
    expect(adjudicationFetch.mock.calls[0][0]).toBe(
      '/v1/agent-eval-campaigns/campaign%2F1/trials/attack%2F1/2/adjudication'
    );

    const comparisonFetch = fetcherReturning(200, {
      baseline_campaign_id: 'baseline',
      challenger_campaign_id: 'challenger',
      rows: []
    });
    await compareAgentCampaigns('k', 'baseline', 'challenger', comparisonFetch);
    expect(comparisonFetch.mock.calls[0][0]).toBe(
      '/v1/agent-eval-comparisons?baseline_campaign_id=baseline&challenger_campaign_id=challenger'
    );

    const exportFetch = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        new Response('campaign,trial\\n', { status: 200 })
    );
    const exportBlob = await downloadAgentCampaign('k', 'campaign/1', 'csv', exportFetch);
    expect(await exportBlob.text()).toBe('campaign,trial\\n');
    expect(exportFetch.mock.calls[0][0]).toBe(
      '/v1/agent-eval-campaigns/campaign%2F1/export?format=csv'
    );

    const reviewFetch = fetcherReturning(202, { request_id: 'review-1', seq: 10 });
    await requestAgentReleaseReview(
      'k',
      'template',
      3,
      ['campaign-1'],
      ['evaluation:campaign-1'],
      ['checker'],
      '2026-08-01T00:00:00Z',
      reviewFetch
    );
    expect(JSON.parse(reviewFetch.mock.calls[0][1]?.body as string).reviewers).toEqual(['checker']);

    const deployFetch = fetcherReturning(202, { deployment_id: 'd1', seq: 11 });
    await deployAgentRelease(
      'k',
      {
        template_id: 'template',
        release: 3,
        environment: 'production',
        reason: 'approved evidence'
      },
      deployFetch
    );
    expect(deployFetch.mock.calls[0][0]).toBe('/v1/agent-deployments');
  });

  it('pins case evidence and preserves the assist idempotency header and feedback', async () => {
    const assistFetch = fetcherReturning(202, {
      assist_id: 'assist-1',
      status: 'completed',
      seq: 12
    });
    await requestCaseAgentAssist(
      'k',
      'case/1',
      {
        kind: 'summary',
        template_id: 'template',
        release: 3,
        environment: 'production',
        evidence_ids: ['e1']
      },
      'logical-request-1',
      assistFetch
    );
    expect(assistFetch.mock.calls[0][0]).toBe('/v1/cases/case%2F1/agent-assists');
    expect(new Headers(assistFetch.mock.calls[0][1]?.headers).get('Idempotency-Key')).toBe(
      'logical-request-1'
    );

    const retryFetch = fetcherReturning(200, { event_id: 'retry', seq: 13 });
    await retryAgentAssist(
      'k',
      'assist/1',
      'operator accepts possible duplicate cost',
      true,
      retryFetch
    );
    expect(retryFetch.mock.calls[0][0]).toBe('/v1/agent-assists/assist%2F1/retry');
    expect(JSON.parse(retryFetch.mock.calls[0][1]?.body as string)).toEqual({
      reason: 'operator accepts possible duplicate cost',
      acknowledge_at_least_once: true
    });

    const cancelFetch = fetcherReturning(200, { event_id: 'cancel', seq: 14 });
    await cancelAgentAssist('k', 'assist/1', 'no longer needed', cancelFetch);
    expect(cancelFetch.mock.calls[0][0]).toBe('/v1/agent-assists/assist%2F1/cancel');

    const actionFetch = fetcherReturning(200, { seq: 13 });
    await recordAgentAssistAction(
      'k',
      'assist-1',
      {
        action: 'edited',
        final: { summary: 'reviewed' },
        reason: 'corrected emphasis',
        time_saved_ms: 90_000
      },
      actionFetch
    );
    expect(JSON.parse(actionFetch.mock.calls[0][1]?.body as string)).toEqual({
      assist_id: 'assist-1',
      action: 'edited',
      final: { summary: 'reviewed' },
      reason: 'corrected emphasis',
      time_saved_ms: 90_000
    });
  });

  it('drives deployment, safety, and human-before-call controls through exact routes', async () => {
    const activateFetch = fetcherReturning(200, { seq: 14 });
    await activateAgentDeployment('k', 'deployment/1', activateFetch);
    expect(activateFetch.mock.calls[0][0]).toBe('/v1/agent-deployments/deployment%2F1/activate');

    const pauseFetch = fetcherReturning(200, { seq: 15 });
    await pauseAgentDeployment('k', 'deployment/1', 'contain incident', pauseFetch);
    expect(JSON.parse(pauseFetch.mock.calls[0][1]?.body as string)).toEqual({
      reason: 'contain incident'
    });

    const resumeFetch = fetcherReturning(200, { seq: 16 });
    await resumeAgentDeployment('k', 'deployment/1', 'incident resolved', resumeFetch);
    expect(resumeFetch.mock.calls[0][0]).toBe('/v1/agent-deployments/deployment%2F1/resume');
    expect(JSON.parse(resumeFetch.mock.calls[0][1]?.body as string)).toEqual({
      reason: 'incident resolved'
    });

    const rollbackFetch = fetcherReturning(200, { seq: 17 });
    await rollbackAgentDeployment('k', 'deployment/1', 2, 'quality regression', rollbackFetch);
    expect(JSON.parse(rollbackFetch.mock.calls[0][1]?.body as string)).toEqual({
      to_release: 2,
      reason: 'quality regression'
    });

    const retireFetch = fetcherReturning(200, { seq: 17 });
    await retireAgentRelease('k', 'template/1', 3, 'superseded', retireFetch);
    expect(retireFetch.mock.calls[0][0]).toBe('/v1/agent-templates/template%2F1/releases/3/retire');

    const incidentListFetch = fetcherReturning(200, {
      incidents: [{ incident_id: 'incident-1', status: 'open' }]
    });
    expect(await listAgentSafetyIncidents('k', incidentListFetch)).toHaveLength(1);

    const incidentOpenFetch = fetcherReturning(201, { incident_id: 'incident-1', seq: 18 });
    await openAgentSafetyIncident(
      'k',
      {
        template_id: 'template',
        release: 3,
        kind: 'prompt_injection',
        severity: 'critical',
        summary: 'Injection crossed a safety boundary'
      },
      incidentOpenFetch
    );
    expect(incidentOpenFetch.mock.calls[0][0]).toBe('/v1/agent-safety-incidents');

    const incidentResolveFetch = fetcherReturning(200, { seq: 19 });
    await resolveAgentSafetyIncident('k', 'incident/1', 'contained', incidentResolveFetch);
    expect(incidentResolveFetch.mock.calls[0][0]).toBe(
      '/v1/agent-safety-incidents/incident%2F1/resolve'
    );

    const approvalsFetch = fetcherReturning(200, {
      approvals: [{ approval_id: 'approval-1', status: 'pending' }]
    });
    expect(await listAgentToolApprovals('k', approvalsFetch)).toHaveLength(1);

    const decisionFetch = fetcherReturning(200, {
      approval_id: 'approval-1',
      assist_id: 'assist-1',
      status: 'requested',
      seq: 20
    });
    await decideAgentToolApproval(
      'k',
      'approval/1',
      'approved',
      'necessary and proportionate',
      decisionFetch
    );
    expect(decisionFetch.mock.calls[0][0]).toBe('/v1/agent-tool-approvals/approval%2F1/decision');
    expect(JSON.parse(decisionFetch.mock.calls[0][1]?.body as string)).toEqual({
      decision: 'approved',
      reason: 'necessary and proportionate'
    });

    const analyticsFetch = fetcherReturning(200, {
      totals: { assists: 2, adoption_rate: 0.5 },
      groups: [],
      segments: []
    });
    const analytics = await getAgentGovernanceAnalytics('k', analyticsFetch);
    expect(analytics.totals.assists).toBe(2);
    expect(analyticsFetch.mock.calls[0][0]).toBe('/v1/agent-governance/analytics');
  });

  it('listAgents unwraps the agents array', async () => {
    const fetcher = fetcherReturning(200, { agents: [{ name: 'triage', runs: 0 }] });
    const a = await listAgents('k', fetcher);
    expect(a).toHaveLength(1);
    expect(a[0].name).toBe('triage');
  });

  it('defineAgent posts provider, schema, and tools', async () => {
    const fetcher = fetcherReturning(201, {});
    await defineAgent(
      'k',
      {
        name: 'triage',
        provider: 'openai',
        model: 'gpt',
        system: 'be terse',
        schema: { type: 'object', required: ['risk'] },
        tools: ['bureau']
      },
      fetcher
    );
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/agents');
    expect(JSON.parse(String(init?.body))).toMatchObject({
      name: 'triage',
      provider: 'openai',
      tools: ['bureau'],
      schema: { type: 'object', required: ['risk'] }
    });
  });

  it('runAgent posts the prompt to the run endpoint', async () => {
    const fetcher = fetcherReturning(200, { run_id: 'r1', status: 'completed', text: 'stub: hi' });
    const res = await runAgent('k', 'triage', 'hi', 0, fetcher);
    expect(res.run_id).toBe('r1');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/agents/triage/run');
    expect(init?.body).toBe(JSON.stringify({ prompt: 'hi' }));
  });

  it('starts a durable agent run with retry and tracking controls', async () => {
    const fetcher = fetcherReturning(202, { run_id: 'r2', status: 'running', seq: 17 });
    const res = await startAgentRun(
      'k',
      'triage',
      'review this',
      {
        version: 2,
        timeoutMs: 45_000,
        maxAttempts: 4,
        idempotencyKey: 'job-2',
        businessReference: 'case-2',
        correlationId: 'trace-2'
      },
      fetcher
    );
    expect(res.status).toBe('running');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/agents/triage/run');
    expect(init?.headers).toMatchObject({
      'Idempotency-Key': 'job-2',
      'X-Correlation-ID': 'trace-2'
    });
    expect(JSON.parse(init?.body as string)).toEqual({
      prompt: 'review this',
      async: true,
      version: 2,
      timeout_ms: 45000,
      max_attempts: 4,
      business_reference: 'case-2',
      correlation_id: 'trace-2'
    });
  });

  it('requests cancellation and an acknowledged dead-letter retry', async () => {
    const cancelFetch = fetcherReturning(202, { status: 'cancelling', seq: 18 });
    await cancelAgentRun('k', 'r2', cancelFetch);
    expect(cancelFetch.mock.calls[0][0]).toBe('/v1/agent-runs/r2/cancel');
    expect(cancelFetch.mock.calls[0][1]?.method).toBe('POST');

    const retryFetch = fetcherReturning(202, { status: 'retrying', seq: 19 });
    await retryAgentRun('k', 'r2', 'provider reconciled', true, retryFetch);
    expect(retryFetch.mock.calls[0][0]).toBe('/v1/agent-runs/r2/retry');
    expect(JSON.parse(retryFetch.mock.calls[0][1]?.body as string)).toEqual({
      reason: 'provider reconciled',
      acknowledge_at_least_once: true
    });
  });

  it('escalateRun posts the case fields and returns the case id', async () => {
    const fetcher = fetcherReturning(202, { case_id: 'c1' });
    const res = await escalateRun(
      'k',
      'triage',
      'r1',
      { company_name: 'Acme', case_type: 'aml', sla_days: 3 },
      fetcher
    );
    expect(res.case_id).toBe('c1');
    const [url] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/agents/triage/runs/r1/escalate');
  });

  it('getRunSummary hits the summary endpoint', async () => {
    const fetcher = fetcherReturning(200, {
      total: 2,
      completed: 1,
      failed: 1,
      by_agent: { triage: 2 }
    });
    const sum = await getRunSummary('k', fetcher);
    expect(sum.total).toBe(2);
    expect(sum.failed).toBe(1);
    const [url] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/agent-runs/summary');
  });
});

describe('decision coverage', () => {
  it('sends explicit record-free dependency mocks', async () => {
    const fetcher = fetcherReturning(200, {
      runs: 10,
      failed_runs: 0,
      fields: ['amount'],
      nodes: [],
      branches: [],
      dispositions: { approve: 0, decline: 0, refer: 0 },
      dead_nodes: [],
      dead_branches: []
    });
    const report = await flowCoverage(
      'k',
      'flow-1',
      {
        runs: 10,
        mock_data: { connect: { bureau: { score: 80 } } }
      },
      fetcher
    );
    expect(report.failed_runs).toBe(0);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/flows/flow-1/coverage');
    expect(JSON.parse(init?.body as string)).toEqual({
      runs: 10,
      mock_data: { connect: { bureau: { score: 80 } } }
    });
  });
});

describe('model actuals', () => {
  it('sends immutable decision lineage and no caller-authored probability', async () => {
    const fetcher = fetcherReturning(202, { event_id: 'e1', seq: 42 });
    await recordModelOutcome(
      'k',
      'risk/v2',
      { decision_id: 'd1', node_id: 'predict-risk', label: 1 },
      fetcher
    );
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/models/risk%2Fv2/outcomes');
    expect(JSON.parse(init?.body as string)).toEqual({
      decision_id: 'd1',
      node_id: 'predict-risk',
      label: 1
    });
  });

  it('selects corrected business outcomes by exact experiment cohort', async () => {
    const fetcher = fetcherReturning(200, { model: 'risk', count: 0, calibration: [] });
    await getModelPerformance(
      'k',
      'risk/v2',
      {
        outcome_key: 'defaulted_90d',
        node_id: 'predict-risk',
        environment: 'production',
        experiment_id: 'exp-1',
        cohort: 3,
        arm: 'challenger'
      },
      fetcher
    );
    const [url] = fetcher.mock.calls[0];
    expect(url).toBe(
      '/v1/models/risk%2Fv2/performance?outcome_key=defaulted_90d&node_id=predict-risk&env=production&experiment=exp-1&cohort=3&arm=challenger'
    );
  });
});

describe('experimentation and population automation', () => {
  it('creates an exact-version cohort and starts it', async () => {
    const spec = {
      name: 'Offer lift',
      hypothesis: 'The challenger lifts conversion',
      owner: 'product',
      flow_id: 'flow-1',
      environment: 'sandbox' as const,
      subject_key_expression: 'customer_id',
      salt: 'salt-1',
      arms: [
        {
          key: 'champion',
          name: 'Champion',
          kind: 'champion' as const,
          version: 1,
          allocation_bps: 5000
        },
        {
          key: 'challenger',
          name: 'Challenger',
          kind: 'challenger' as const,
          version: 2,
          allocation_bps: 5000
        }
      ],
      primary_metric: {
        key: 'converted',
        name: 'Conversion',
        kind: 'binary' as const,
        direction: 'increase' as const
      },
      minimum_sample_per_arm: 100,
      minimum_effect: 0.01,
      confidence: 0.95,
      observation_window_days: 30
    };
    const createFetch = fetcherReturning(201, { experiment_id: 'exp-1', seq: 8 });
    await createExperiment('k', spec, createFetch);
    expect(createFetch.mock.calls[0][0]).toBe('/v1/experiments');
    expect(JSON.parse(createFetch.mock.calls[0][1]?.body as string)).toEqual(spec);

    const startFetch = fetcherReturning(202, { status: 'running', seq: 9 });
    await transitionExperiment('k', 'exp/1', 'start', '', startFetch);
    expect(startFetch.mock.calls[0][0]).toBe('/v1/experiments/exp%2F1/start');
  });

  it('requires an idempotency header for observed outcomes and population jobs', async () => {
    const outcomeFetch = fetcherReturning(202, { outcome_id: 'out-1', revision: 1, seq: 10 });
    await recordOutcome(
      'k',
      {
        decision_id: 'd1',
        key: 'converted',
        kind: 'binary',
        value: 1,
        event_time: '2026-07-30T10:00:00Z',
        source: { system: 'loan-core', record_id: 'loan-1' },
        label_version: 'v1'
      },
      'outcome-key',
      outcomeFetch
    );
    expect(outcomeFetch.mock.calls[0][1]?.headers).toMatchObject({
      'Idempotency-Key': 'outcome-key'
    });

    const jobFetch = fetcherReturning(202, { job_id: 'job-1', state: 'queued', seq: 11 });
    await createPopulationJob(
      'k',
      {
        kind: 'backtest',
        slug: 'offers',
        environment: 'sandbox',
        items: [{ data: { customer_id: 'c1' } }]
      },
      'job-key',
      jobFetch
    );
    expect(jobFetch.mock.calls[0][0]).toBe('/v1/population-jobs');
    expect(jobFetch.mock.calls[0][1]?.headers).toMatchObject({ 'Idempotency-Key': 'job-key' });
  });
});

describe('session auth', () => {
  it('login posts the api key and returns the identity', async () => {
    const fetcher = fetcherReturning(200, { org: 'demo', workspace: 'main', actor: 'dev' });
    const id = await login('dev-sandbox-key', fetcher);
    expect(id.actor).toBe('dev');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/login');
    expect(init?.body).toBe(JSON.stringify({ api_key: 'dev-sandbox-key' }));
  });

  it('login surfaces an invalid key', async () => {
    await expect(
      login('nope', fetcherReturning(401, { error: 'invalid api key' }))
    ).rejects.toThrow(/invalid api key/);
  });

  it('currentUser returns null when unauthenticated', async () => {
    expect(await currentUser(fetcherReturning(401, {}))).toBeNull();
  });

  it('listSsoProviders distinguishes a valid empty list from discovery failure', async () => {
    expect(await listSsoProviders(fetcherReturning(200, { providers: ['google', 'aws'] }))).toEqual(
      ['google', 'aws']
    );
    expect(await listSsoProviders(fetcherReturning(200, { providers: [] }))).toEqual([]);
    await expect(
      listSsoProviders(fetcherReturning(503, { error: 'identity unavailable' }))
    ).rejects.toThrow(/identity unavailable/);
    await expect(listSsoProviders(fetcherReturning(200, {}))).rejects.toThrow(
      /invalid provider list/
    );
  });

  it('listSamlProviders distinguishes a valid empty list from discovery failure', async () => {
    expect(await listSamlProviders(fetcherReturning(200, { providers: ['okta'] }))).toEqual([
      'okta'
    ]);
    expect(await listSamlProviders(fetcherReturning(200, { providers: [] }))).toEqual([]);
    await expect(listSamlProviders(fetcherReturning(500, {}))).rejects.toThrow();
  });

  it('currentUser returns the identity when signed in', async () => {
    const id = await currentUser(
      fetcherReturning(200, { org: 'demo', workspace: 'main', actor: 'dev' })
    );
    expect(id?.actor).toBe('dev');
  });

  it('logout posts to the logout endpoint', async () => {
    const fetcher = fetcherReturning(200, { logout_url: 'https://auth.example.test/logout' });
    await expect(logout(fetcher)).resolves.toBe('https://auth.example.test/logout');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/logout');
    expect(init?.method).toBe('POST');
    expect(init?.headers).toEqual({ 'X-Requested-With': 'intraktible' });
  });
});
