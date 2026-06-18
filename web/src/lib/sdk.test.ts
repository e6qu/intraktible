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
    const res = await c.decide('risk', 'production', { data: { amount: 10 }, entity_id: 'e1' });
    expect(res.status).toBe('completed');
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('https://api.example.com/v1/flows/risk/production/decide');
    expect(init?.method).toBe('POST');
    expect(JSON.parse(init?.body as string)).toEqual({ data: { amount: 10 }, entity_id: 'e1' });
    expect(init?.headers).toMatchObject({ 'Content-Type': 'application/json' });
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
      slug: 'demo',
      version: 1,
      created: true,
      published: true
    });
    const c = new Client({ apiKey: 'k', fetch: fetcher });
    const out = await c.importFlow({ slug: 'demo', graph: { nodes: [], edges: [] } });
    expect(out.version).toBe(1);
    expect(fetcher.mock.calls[0][0]).toBe('/v1/flows/import');
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
    const fetcher = fetcherReturning(200, {
      results: [{ slug: 'a', created: true, published: true }],
      published: 1,
      failed: 0,
      unchanged: 0
    });
    const c = new Client({ apiKey: 'k', fetch: fetcher });
    const out = await c.importBundle([{ slug: 'a', graph: {} }]);
    expect(out.published).toBe(1);
    expect(fetcher.mock.calls[0][0]).toBe('/v1/flows/import-bundle');
    expect(JSON.parse(fetcher.mock.calls[0][1]?.body as string)).toEqual({
      flows: [{ slug: 'a', graph: {} }]
    });
  });

  it('unwraps list endpoints and defaults to empty', async () => {
    const c = new Client({ apiKey: 'k', fetch: fetcherReturning(200, {}) });
    expect(await c.listDecisions()).toEqual([]);
    expect(await c.listFlows()).toEqual([]);
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
