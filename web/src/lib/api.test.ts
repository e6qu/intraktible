// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, vi } from 'vitest';
import {
  getStats,
  sayHello,
  listFlows,
  createFlow,
  decide,
  publishVersion,
  listCases,
  getCaseSummary,
  requestReview,
  assignCase,
  setCaseStatus
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
