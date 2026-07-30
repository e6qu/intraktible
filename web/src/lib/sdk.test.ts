// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, it, expect, vi } from 'vitest';
import { Client, ApiError } from './sdk';

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

describe('sdk Client', () => {
  it('sends the api key and parses me()', async () => {
    const fetcher = fetcherReturning(200, {
      org: 'o',
      workspace: 'w',
      actor: 'ada',
      scope: 'sandbox',
      role: 'admin'
    });
    const c = new Client({ apiKey: 'secret', fetch: fetcher });
    const me = await c.me();
    expect(me.actor).toBe('ada');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/me');
    expect(init?.headers).toMatchObject({ 'X-Api-Key': 'secret' });
  });

  it('prefixes baseUrl and posts the decide path + body', async () => {
    const fetcher = fetcherReturning(200, {
      decision_id: 'd1',
      status: 'completed',
      data: { ok: true }
    });
    const c = new Client({ apiKey: 'k', baseUrl: 'https://api.example.com/', fetch: fetcher });
    const res = await c.decide('risk', 'production', {
      data: { amount: 10 },
      entity_id: 'e1',
      business_reference: 'app-42',
      correlation_id: 'trace-7',
      metadata: { channel: 'mobile' },
      control: { timeout_ms: 750 },
      idempotencyKey: 'retry-42'
    });
    expect(res.status).toBe('completed');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('https://api.example.com/v1/flows/risk/production/decide');
    expect(init?.method).toBe('POST');
    expect(JSON.parse(init?.body as string)).toEqual({
      data: { amount: 10 },
      entity_id: 'e1',
      business_reference: 'app-42',
      correlation_id: 'trace-7',
      metadata: { channel: 'mobile' },
      control: { timeout_ms: 750 }
    });
    expect(init?.headers).toMatchObject({
      'Content-Type': 'application/json',
      'Idempotency-Key': 'retry-42'
    });
  });

  it('decideBatch wraps the dataset', async () => {
    const fetcher = fetcherReturning(200, { summary: { total: 2 }, results: [{}, {}] });
    const c = new Client({ apiKey: 'k', fetch: fetcher });
    const out = await c.decideBatch('risk', 'sandbox', [{ a: 1 }, { a: 2 }]);
    expect(out.results).toHaveLength(2);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/v1/flows/risk/sandbox/decide/batch');
    expect(JSON.parse(init?.body as string)).toEqual({ dataset: [{ a: 1 }, { a: 2 }] });
  });

  it('createFlow returns the new flow id', async () => {
    const fetcher = fetcherReturning(201, { flow_id: 'f9' });
    const c = new Client({ apiKey: 'k', fetch: fetcher });
    expect(await c.createFlow('demo', 'Demo')).toBe('f9');
    expect(JSON.parse(fetcher.mock.calls[0][1]?.body as string)).toEqual({
      slug: 'demo',
      name: 'Demo'
    });
  });

  it('importFlow posts a flow-as-code document', async () => {
    const fetcher = fetcherReturning(201, {
      flow_id: 'f9',
      draft_id: 'd1',
      revision: 1,
      created: true
    });
    const c = new Client({ apiKey: 'k', fetch: fetcher });
    const out = await c.importFlow({ slug: 'demo', graph: { nodes: [], edges: [] } }, 'commit-1');
    expect(out.revision).toBe(1);
    expect(fetcher.mock.calls[0][0]).toBe('/v1/authoring/import');
    expect(fetcher.mock.calls[0][1]?.headers).toMatchObject({
      'Idempotency-Key': 'commit-1'
    });
  });

  it('creates component upgrade drafts with an explicit retry identity', async () => {
    const fetcher = fetcherReturning(201, {
      drafts: [{ flow_id: 'f1', draft_id: 'd1', base_version: 3, revision: 1 }],
      seq: 8
    });
    const c = new Client({ apiKey: 'k', fetch: fetcher });
    const result = await c.createComponentUpgradeDrafts(
      'component/1',
      { from_version: 1, to_version: 2, flow_ids: ['f1'] },
      'upgrade-key'
    );
    expect(result.drafts[0].draft_id).toBe('d1');
    expect(fetcher.mock.calls[0][0]).toBe('/v1/authoring/components/component%2F1/upgrade-drafts');
    expect(fetcher.mock.calls[0][1]?.headers).toMatchObject({
      'Idempotency-Key': 'upgrade-key'
    });
  });

  it('deploy and promote post to the right endpoints', async () => {
    const deployFetcher = fetcherReturning(201, { environment: 'sandbox', version: 2 });
    const dc = new Client({ apiKey: 'k', fetch: deployFetcher });
    await dc.deploy('f1', 'sandbox', 2);
    const [durl, dinit] = deployFetcher.mock.calls[0];
    expect(durl).toBe('/v1/flows/f1/deployments');
    expect(JSON.parse(dinit?.body as string)).toEqual({ environment: 'sandbox', version: 2 });

    const promFetcher = fetcherReturning(200, { promoted: true, version: 2 });
    const pc = new Client({ apiKey: 'k', fetch: promFetcher });
    const prom = await pc.promote('f1', 'sandbox', 'staging');
    expect(prom.promoted).toBe(true);
    expect(JSON.parse(promFetcher.mock.calls[0][1]?.body as string)).toEqual({
      from: 'sandbox',
      to: 'staging',
      force: false
    });
  });

  it('importBundle wraps the flows and returns the report', async () => {
    const fetcher = fetcherReturning(201, {
      imports: [{ flow_id: 'f1', draft_id: 'd1', revision: 1, created: true }],
      seq: 4
    });
    const c = new Client({ apiKey: 'k', fetch: fetcher });
    const out = await c.importBundle([{ slug: 'a', graph: {} }], 'bundle-1');
    expect(out.imports).toHaveLength(1);
    expect(fetcher.mock.calls[0][0]).toBe('/v1/authoring/import-bundle');
    expect(JSON.parse(fetcher.mock.calls[0][1]?.body as string)).toEqual({
      format_version: 'intraktible.authoring/v1',
      kind: 'bundle',
      flows: [
        {
          slug: 'a',
          graph: {},
          format_version: 'intraktible.authoring/v1',
          kind: 'flow'
        }
      ]
    });
  });

  it('covers the complete collaborative draft lifecycle', async () => {
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
        const path = String(input);
        if (path.endsWith('/revisions')) {
          return jsonResponse(200, { revisions: [{ draft_id: 'd1', revision: 2 }] });
        }
        if (path.endsWith('/presence') && init?.method === 'PUT') {
          return jsonResponse(200, { draft_id: 'd1', actor: 'ada', revision: 2 });
        }
        if (path.endsWith('/presence') && init?.method === 'GET') {
          return jsonResponse(200, {
            presence: [{ draft_id: 'd1', actor: 'ada', revision: 2 }]
          });
        }
        if (path.endsWith('/presence') && init?.method === 'DELETE') {
          return new Response(null, { status: 204 });
        }
        if (path.endsWith('/rebase')) return jsonResponse(200, { revision: 3 });
        if (init?.method === 'DELETE') return jsonResponse(200, { event_id: 'archived' });
        return jsonResponse(200, { draft_id: 'd1', revision: 2 });
      }
    );
    const c = new Client({ apiKey: 'k', fetch: fetcher });
    expect((await c.getDraft('d1')).revision).toBe(2);
    expect(
      (
        await c.rebaseDraft('d1', {
          expected_revision: 2,
          base_version: 1,
          title: 'Resolved',
          graph: {}
        })
      ).revision
    ).toBe(3);
    expect(await c.listDraftRevisions('d1')).toHaveLength(1);
    expect((await c.renewDraftPresence('d1', { revision: 2 })).actor).toBe('ada');
    expect(await c.listDraftPresence('d1')).toHaveLength(1);
    await expect(c.leaveDraftPresence('d1')).resolves.toBeUndefined();
    await expect(c.archiveDraft('d1')).resolves.toBeUndefined();
  });

  it('unwraps list endpoints and defaults to empty', async () => {
    const c = new Client({ apiKey: 'k', fetch: fetcherReturning(200, {}) });
    expect(await c.listDecisions()).toEqual([]);
    expect(await c.listFlows()).toEqual([]);
  });

  it('sends experiment, outcome, and population idempotency contracts', async () => {
    const outcomeFetcher = fetcherReturning(202, { outcome_id: 'out-1', revision: 1 });
    const outcomeClient = new Client({ apiKey: 'k', fetch: outcomeFetcher });
    await outcomeClient.recordOutcome(
      {
        decision_id: 'd1',
        key: 'converted',
        kind: 'binary',
        value: 1,
        event_time: '2026-07-30T10:00:00Z',
        source: { system: 'core', record_id: '1' },
        label_version: 'v1'
      },
      'outcome-key'
    );
    expect(outcomeFetcher.mock.calls[0][0]).toBe('/v1/outcomes');
    expect(outcomeFetcher.mock.calls[0][1]?.headers).toMatchObject({
      'Idempotency-Key': 'outcome-key'
    });

    const populationFetcher = fetcherReturning(202, { job_id: 'job-1' });
    const populationClient = new Client({ apiKey: 'k', fetch: populationFetcher });
    expect(
      await populationClient.createPopulationJob(
        {
          kind: 'backtest',
          slug: 'offers',
          environment: 'sandbox',
          items: [{ data: { customer_id: 'c1' } }]
        },
        'job-key'
      )
    ).toBe('job-1');
    expect(populationFetcher.mock.calls[0][1]?.headers).toMatchObject({
      'Idempotency-Key': 'job-key'
    });
  });

  it('throws a typed ApiError carrying the server message', async () => {
    const c = new Client({
      apiKey: 'k',
      fetch: fetcherReturning(404, { error: 'flow not found' })
    });
    await expect(c.getFlow('missing')).rejects.toBeInstanceOf(ApiError);
    try {
      await c.getFlow('missing');
    } catch (e) {
      expect((e as ApiError).status).toBe(404);
      expect((e as ApiError).message).toBe('flow not found');
    }
  });

  it('falls back to the status when the error body is not JSON', async () => {
    const fetcher = vi.fn(async (): Promise<Response> => new Response('nope', { status: 500 }));
    const c = new Client({ apiKey: 'k', fetch: fetcher });
    await expect(c.me()).rejects.toMatchObject({ status: 500, message: '500' });
  });
});
