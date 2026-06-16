// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, vi } from 'vitest';
import {
  getStats,
  sayHello,
  listFlows,
  createFlow,
  decide,
  publishVersion,
  exportFlow,
  exportDecision,
  listDecisions,
  getDecision,
  getFlowMetrics,
  backtestFlow,
  deployVersion,
  requestDeployment,
  approveDeployment,
  rejectDeployment,
  listAudit,
  auditQuery,
  auditExportUrl,
  listConnectors,
  defineConnector,
  listFeatures,
  defineFeature,
  listEntities,
  listEntityEvents,
  listCases,
  getCaseSummary,
  requestReview,
  assignCase,
  setCaseStatus,
  listAgents,
  runAgent,
  escalateRun,
  getRunSummary,
  login,
  logout,
  currentUser
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
    const out = await exportDecision('k', 'd9', fetcher);
    expect(out).toBe('sequenceDiagram\n');
    expect(fetcher.mock.calls[0][0]).toBe('/v1/decisions/d9/export');
  });

  it('throws loudly on a non-2xx export', async () => {
    await expect(exportFlow('k', 'f1', 'mermaid', textFetcher(404, ''))).rejects.toThrow(/404/);
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
    await approveDeployment('k', 'f1', 'req-9', fetcher);
    expect(fetcher.mock.calls[0][0]).toBe('/v1/flows/f1/deployment-requests/req-9/approve');
  });

  it('surfaces the four-eyes self-approval error loudly', async () => {
    await expect(
      approveDeployment(
        'k',
        'f1',
        'req-9',
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

  it('createFlow posts slug and name', async () => {
    const fetcher = fetcherReturning(201, { flow_id: 'f1' });
    const res = await createFlow('k', 'my-flow', 'My Flow', fetcher);
    expect(res.flow_id).toBe('f1');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/flows');
    expect(init?.method).toBe('POST');
    expect(init?.body).toBe(JSON.stringify({ slug: 'my-flow', name: 'My Flow' }));
  });

  it('publishVersion posts the graph and returns the version', async () => {
    const fetcher = fetcherReturning(201, { version: 2, etag: 'abc' });
    const res = await publishVersion('k', 'f1', { nodes: [], edges: [] }, fetcher);
    expect(res.version).toBe(2);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/flows/f1/versions');
    expect(init?.body).toBe(JSON.stringify({ graph: { nodes: [], edges: [] } }));
  });

  it('publishVersion surfaces the backend validation error', async () => {
    const fetcher = fetcherReturning(400, { error: 'graph needs exactly one input node' });
    await expect(publishVersion('k', 'f1', { nodes: [], edges: [] }, fetcher)).rejects.toThrow(
      /exactly one input/
    );
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

  it('assignCase posts to the assign action', async () => {
    const fetcher = fetcherReturning(202, {});
    await assignCase('k', 'c1', 'adam', fetcher);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/cases/c1/assign');
    expect(init?.body).toBe(JSON.stringify({ assignee: 'adam' }));
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
});

describe('agents', () => {
  it('listAgents unwraps the agents array', async () => {
    const fetcher = fetcherReturning(200, { agents: [{ name: 'triage', runs: 0 }] });
    const a = await listAgents('k', fetcher);
    expect(a).toHaveLength(1);
    expect(a[0].name).toBe('triage');
  });

  it('runAgent posts the prompt to the run endpoint', async () => {
    const fetcher = fetcherReturning(200, { run_id: 'r1', status: 'completed', text: 'stub: hi' });
    const res = await runAgent('k', 'triage', 'hi', fetcher);
    expect(res.run_id).toBe('r1');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/agents/triage/run');
    expect(init?.body).toBe(JSON.stringify({ prompt: 'hi' }));
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

  it('currentUser returns the identity when signed in', async () => {
    const id = await currentUser(
      fetcherReturning(200, { org: 'demo', workspace: 'main', actor: 'dev' })
    );
    expect(id?.actor).toBe('dev');
  });

  it('logout posts to the logout endpoint', async () => {
    const fetcher = fetcherReturning(200, {});
    await logout(fetcher);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/logout');
    expect(init?.method).toBe('POST');
  });
});
