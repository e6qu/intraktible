// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, vi } from 'vitest';
import { getStats, sayHello } from './api';

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
