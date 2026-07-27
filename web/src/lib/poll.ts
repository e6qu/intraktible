// SPDX-License-Identifier: AGPL-3.0-or-later

// pollUntil waits for an eventually-consistent projection to expose a command
// result. Commands acknowledge the durable event before the read model applies
// it; a one-shot re-fetch can therefore leave a successful action invisible.
// Read failures still fail immediately. Only a healthy-but-not-yet-applied value
// is retried, under a bounded wait.
export async function pollUntil<T>(
  read: () => Promise<T>,
  ready: (value: T) => boolean,
  label: string,
  attempts = 40
): Promise<T> {
  let delayMs = 20;
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    const value = await read();
    if (ready(value)) return value;
    if (attempt + 1 < attempts) {
      await new Promise((resolve) => setTimeout(resolve, delayMs));
      delayMs = Math.min(250, delayMs * 2);
    }
  }
  throw new Error(`${label} was recorded but did not appear in the read model in time.`);
}

// waitForApplied uses the public readiness progress to establish read-after-write
// consistency for a specific event. /readyz may remain HTTP 200 under ordinary
// steady-state lag, so the applied sequence in its body—not the status code—is
// the contract checked here.
export async function waitForApplied(seq: number, fetcher: typeof fetch = fetch): Promise<void> {
  await pollUntil(
    async () => {
      const response = await fetcher('/readyz');
      const body = (await response.json()) as {
        status?: string;
        applied?: number;
        error?: string;
      };
      if (body.status === 'degraded') {
        throw new Error(body.error || 'Projection runtime is degraded.');
      }
      if (!response.ok && body.status !== 'rebuilding' && body.status !== 'draining') {
        throw new Error(body.error || `Read-model readiness failed: ${response.status}`);
      }
      return body.applied ?? -1;
    },
    (applied) => applied >= seq,
    `Event ${seq}`
  );
}
