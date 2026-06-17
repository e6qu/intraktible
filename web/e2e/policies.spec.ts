// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('creates a policy bound to a flow and publishes a band', async ({ page, request }) => {
  const slug = 'pol-' + Math.random().toString(36).slice(2, 8);
  await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: `PolFlow ${slug}` }
  });

  await page.goto('/policies');
  await expect(page.getByRole('heading', { name: 'Policies' })).toBeVisible();

  await page.getByLabel('policy name').fill(`stp-${slug}`);
  await page.getByLabel('flow', { exact: true }).selectOption(slug);
  await page.getByRole('button', { name: /Create policy/ }).click();

  // The band editor opens for the new policy; add an auto-approve band and publish.
  const editor = page.getByTestId('band-editor');
  await expect(editor).toBeVisible();
  await page.getByRole('button', { name: 'Add band' }).click();
  await page.getByLabel('band 0 when').fill('score >= 0.85');
  await page.getByLabel('band 0 disposition').selectOption('approve');
  await page.getByTestId('publish-policy').click();

  await expect(page.getByText(/Published policy v1/)).toBeVisible();
  // The list now shows the published version.
  await expect(page.locator('tr', { hasText: `stp-${slug}` })).toContainText('v1');
});
