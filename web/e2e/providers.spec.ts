// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('an integration owner installs, tests, approves, and deploys a provider', async ({
  page,
  request
}) => {
  const suffix = Math.random().toString(36).slice(2, 8);
  const name = `e2e-provider-${suffix}`;

  // Install a provider version through the real API.
  const install = await request.post('/v1/providers', {
    headers: { 'X-Api-Key': KEY },
    data: {
      name,
      connector: 'credit-bureau',
      description: `E2E provider ${suffix}`,
      conformance: {
        schema: '{"type":"object","properties":{"score":{"type":"number"}}}',
        timeout_seconds: 10,
        max_retries: 2,
        cost_per_fetch_usd: 0.05
      }
    }
  });
  expect(install.ok()).toBeTruthy();
  const { version } = (await install.json()) as { version: number };
  expect(version).toBe(1);

  // Configure for production, then conformance-test.
  const configure = await request.post(`/v1/providers/${name}/1/configure`, {
    headers: { 'X-Api-Key': KEY },
    data: { environment: 'production', config: { url: 'https://bureau.example' } }
  });
  expect(configure.ok()).toBeTruthy();
  const tested = await request.post(`/v1/providers/${name}/1/test`, {
    headers: { 'X-Api-Key': KEY },
    data: { passed: true, fixture: 'sandbox', details: 'ok' }
  });
  expect(tested.ok()).toBeTruthy();

  // The installer cannot self-approve; a second approver can.
  const selfApprove = await request.post(`/v1/providers/${name}/1/approve`, {
    headers: { 'X-Api-Key': KEY },
    data: { request_id: `req-${suffix}`, reason: 'self approval must be refused' }
  });
  expect(selfApprove.ok()).toBeFalsy();
  const checker = await request.post('/v1/api-keys', {
    headers: { 'X-Api-Key': KEY },
    data: { name: `checker-${suffix}`, actor: `checker-${suffix}`, role: 'approver', scope: '*' }
  });
  const { secret } = (await checker.json()) as { secret: string };
  const approved = await request.post(`/v1/providers/${name}/1/approve`, {
    headers: { 'X-Api-Key': secret },
    data: { request_id: `req-${suffix}`, reason: 'independent review passed' }
  });
  expect(approved.ok()).toBeTruthy();

  // Deploy to production.
  const deployed = await request.post(`/v1/providers/${name}/1/deploy`, {
    headers: { 'X-Api-Key': secret },
    data: { environment: 'production' }
  });
  expect(deployed.ok()).toBeTruthy();

  // The providers page shows the version with its lifecycle state.
  await page.goto('/providers');
  await expect(page.getByRole('heading', { name: 'Providers' })).toBeVisible();
  const panel = page.getByRole('region', { name: 'Provider versions' });
  const card = panel.locator('li.card', { hasText: `${name} v1` }).first();
  await expect(card).toBeVisible({ timeout: 15_000 });
  await expect(card.getByText('production: deployed')).toBeVisible();
});
