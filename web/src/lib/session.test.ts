// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, vi, afterEach } from 'vitest';
import { get } from 'svelte/store';
import { user, refreshUser, signOut } from './session';
import type { Identity } from './api';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' }
  });
}

const signedIn: Identity = { org: 'acme', workspace: 'main', actor: 'jo', role: 'admin' };

afterEach(() => {
  vi.unstubAllGlobals();
  user.set(null);
});

describe('refreshUser', () => {
  it('sets the identity from /v1/me on success', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse(200, signedIn))
    );
    await refreshUser();
    expect(get(user)).toEqual(signedIn);
  });

  it('clears the session only on a real 401', async () => {
    user.set(signedIn);
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse(401, {}))
    );
    await refreshUser();
    expect(get(user)).toBeNull();
  });

  it('keeps the existing session and rethrows on a transient (non-401) failure', async () => {
    // A momentary /v1/me blip (network/5xx) must NOT visually log the user out.
    user.set(signedIn);
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse(500, { error: 'upstream down' }))
    );
    await expect(refreshUser()).rejects.toThrow();
    expect(get(user)).toEqual(signedIn);
  });
});

describe('signOut', () => {
  it('clears the session store after revoking the session', async () => {
    user.set(signedIn);
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse(200, { logout_url: '' }))
    );
    await signOut();
    expect(get(user)).toBeNull();
  });

  it('clears the client identity when logout fails so protected UI cannot fail open', async () => {
    user.set(signedIn);
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse(503, { error: 'session store unavailable' }))
    );
    await expect(signOut()).rejects.toThrow(/session store unavailable/);
    expect(get(user)).toBeNull();
  });
});
