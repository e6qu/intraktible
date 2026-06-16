// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';
const uniq = () => Math.random().toString(36).slice(2, 9);

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('defines a connector and a feature from the UI', async ({ page }) => {
  const conn = 'conn-' + uniq();
  const feat = 'feat_' + uniq();

  await page.goto('/data');
  await expect(page.getByRole('heading', { name: /Context data/i })).toBeVisible();

  // Define a connector.
  await page.getByLabel('connector name').fill(conn);
  await page.getByLabel('connector type').selectOption('mock_bureau');
  await page.getByRole('button', { name: 'Define connector' }).click();
  await expect(page.locator('tbody').filter({ hasText: conn })).toBeVisible();

  // Define a feature.
  await page.getByLabel('feature name').fill(feat);
  await page.getByLabel('feature entity type').fill('customer');
  await page.getByLabel('feature event name').fill('txn');
  await page.getByLabel('feature aggregation').selectOption('count');
  await page.getByLabel('feature window hours').fill('24');
  await page.getByRole('button', { name: 'Define feature' }).click();
  await expect(page.locator('tbody').filter({ hasText: feat })).toBeVisible();
});

test('browses an entity and its event timeline', async ({ page, request }) => {
  const id = 'cust-' + uniq();
  // Seed an entity + a custom event via the API.
  await request.post('/v1/context/entities', {
    headers: { 'X-Api-Key': KEY },
    data: { entity_type: 'customer', entity_id: id, attributes: { tier: 'gold' } }
  });
  await request.post('/v1/context/events', {
    headers: { 'X-Api-Key': KEY },
    data: { entity_type: 'customer', entity_id: id, event_name: 'login', data: { ip: '1.2.3.4' } }
  });

  await page.goto('/data');
  const row = page.locator('tbody tr').filter({ hasText: id });
  await expect(row).toBeVisible();
  await row.getByRole('link', { name: id }).click();

  // Entity detail shows the attribute and the event timeline.
  await expect(page.getByRole('heading', { level: 1 })).toContainText(id);
  await expect(page.getByText('gold')).toBeVisible();
  await expect(page.locator('.timeline')).toContainText('login');
});
