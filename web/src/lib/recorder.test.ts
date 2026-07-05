// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';
import { get } from 'svelte/store';
import { recordedCalls, resetRecorder } from './recorder';
import { recordingFetch } from './api';

describe('API-call recorder', () => {
  beforeEach(() => {
    resetRecorder();
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('{}', { status: 200 }))
    );
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('records method, host-less path, and status for a plain call', async () => {
    await recordingFetch('/v1/decisions?limit=25&offset=0');
    expect(get(recordedCalls)).toEqual([{ method: 'GET', path: '/v1/decisions', status: 200 }]);
  });

  it('records the init method and the status of the response', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('{}', { status: 403 }))
    );
    await recordingFetch('/v1/flows', { method: 'post' });
    expect(get(recordedCalls)).toEqual([{ method: 'POST', path: '/v1/flows', status: 403 }]);
  });

  it('records a Request input, stripping the host', async () => {
    await recordingFetch(new Request('http://localhost:8080/v1/hello', { method: 'POST' }));
    expect(get(recordedCalls)).toEqual([{ method: 'POST', path: '/v1/hello', status: 200 }]);
  });

  it('caps the per-navigation ring buffer at 50 calls, keeping the newest', async () => {
    for (let i = 0; i < 60; i++) await recordingFetch(`/v1/decisions/${i}`);
    const calls = get(recordedCalls);
    expect(calls).toHaveLength(50);
    expect(calls[0].path).toBe('/v1/decisions/10');
    expect(calls.at(-1)?.path).toBe('/v1/decisions/59');
  });

  it('resets to empty on route change', async () => {
    await recordingFetch('/v1/decisions');
    resetRecorder();
    expect(get(recordedCalls)).toEqual([]);
  });
});
