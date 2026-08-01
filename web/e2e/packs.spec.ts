// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('a domain owner installs, upgrades, rolls back, and retires a solution pack', async ({
  page,
  request
}) => {
  const suffix = Math.random().toString(36).slice(2, 8);
  const name = `e2e-pack-${suffix}`;

  function manifest(upgradeFrom: number[] = []) {
    return {
      name,
      title: `E2E pack ${suffix}`,
      description: 'E2E solution pack journey.',
      signature: 'sig',
      artifacts: [{ kind: 'flow', id: `flow-${suffix}`, content: { graph: '...' } }],
      upgrade_from: upgradeFrom
    };
  }

  // Define and install v1.
  const define = await request.post('/v1/packs', {
    headers: { 'X-Api-Key': KEY },
    data: manifest()
  });
  expect(define.ok()).toBeTruthy();
  const install = await request.post(`/v1/packs/${name}/install`, {
    headers: { 'X-Api-Key': KEY },
    data: { version: 1 }
  });
  expect(install.ok()).toBeTruthy();

  // Define v2 (declared upgrade path) and upgrade.
  const define2 = await request.post('/v1/packs', {
    headers: { 'X-Api-Key': KEY },
    data: manifest([1])
  });
  expect(define2.ok()).toBeTruthy();
  const upgrade = await request.post(`/v1/packs/${name}/upgrade`, {
    headers: { 'X-Api-Key': KEY },
    data: { version: 2 }
  });
  expect(upgrade.ok()).toBeTruthy();

  // Roll back to v1, then retire.
  const rollback = await request.post(`/v1/packs/${name}/rollback`, {
    headers: { 'X-Api-Key': KEY },
    data: { version: 1 }
  });
  expect(rollback.ok()).toBeTruthy();
  const retire = await request.post(`/v1/packs/${name}/retire`, {
    headers: { 'X-Api-Key': KEY },
    data: { reason: 'sunset' }
  });
  expect(retire.ok()).toBeTruthy();

  // The packs page shows the pack and its retired state.
  await page.goto('/packs');
  await expect(page.getByRole('heading', { name: 'Solution packs' })).toBeVisible();
  const panel = page.getByRole('region', { name: 'Solution packs' });
  const card = panel.locator('li.card', { hasText: name }).first();
  await expect(card).toBeVisible({ timeout: 15_000 });
  await expect(card.getByText('retired')).toBeVisible();
});
