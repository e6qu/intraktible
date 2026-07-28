// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from 'vitest';
import { waitForApplied } from './poll';

function ready(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' }
  });
}

describe('waitForApplied', () => {
  it('waits for the requested projection sequence even when readiness is already 200', async () => {
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(ready(200, { status: 'ready', applied: 4 }))
      .mockResolvedValueOnce(ready(200, { status: 'ready', applied: 5 }));

    await waitForApplied(5, fetcher);
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it('fails immediately when the projection runtime is degraded', async () => {
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValue(
        ready(503, { status: 'degraded', applied: 4, error: 'projector context failed' })
      );

    await expect(waitForApplied(5, fetcher)).rejects.toThrow('projector context failed');
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});
