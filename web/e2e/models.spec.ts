// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('defines a predictive model from the registry page', async ({ page }) => {
  await page.goto('/models');
  await expect(page.getByRole('heading', { name: 'Models' })).toBeVisible();

  const name = 'risk-' + Math.random().toString(36).slice(2, 8);
  await page.getByLabel('model name').fill(name);
  // The logistic starter is preloaded; define it.
  await page.getByRole('button', { name: 'Define model' }).click();

  const row = page.locator('tbody tr').filter({ hasText: name });
  await expect(row).toBeVisible();
  await expect(row.getByText('logistic')).toBeVisible();

  // The drift readout opens; with no predictions yet it explains how to get data.
  await row.getByRole('button', { name: 'Drift' }).click();
  const driftRow = page.getByTestId('model-drift');
  await expect(driftRow).toBeVisible();
  await expect(driftRow).toContainText('No predictions recorded yet');
});

test('a predict node panel edits model + output without raw JSON', async ({ page, request }) => {
  const slug = 'pf-' + Math.random().toString(36).slice(2, 8);
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Predict flow' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  await expect(page.getByLabel('new node type')).toBeVisible();
  await page.getByLabel('new node type').selectOption('predict');
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await page.getByLabel('predict model').fill('risk');
  await page.getByLabel('predict output').fill('risk');

  await expect(page.getByLabel('node config')).toHaveValue('{"model":"risk","output":"risk"}');
});
