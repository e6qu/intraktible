// SPDX-License-Identifier: AGPL-3.0-or-later
// The per-page "Export for AI" capability: the guide panel's Copy for AI and the
// header's one-click copy produce a markdown document describing the page — its
// help-registry summary, the API calls recorded this visit, and its content.
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test.use({ permissions: ['clipboard-read', 'clipboard-write'] });

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('Copy for AI in the page guide exports /decisions with its API calls', async ({ page }) => {
  await page.goto('/decisions');
  await expect(page.getByRole('heading', { name: 'Decisions' })).toBeVisible();

  await page.getByTestId('guide-trigger').click();
  await page.getByTestId('guide-copy-ai').click();
  await expect(page.getByText('Copied for AI')).toBeVisible();

  const doc = await page.evaluate(() => navigator.clipboard.readText());
  expect(doc).toContain('# Decisions — intraktible page export');
  expect(doc).toContain('Every decision the engine has run'); // the page summary
  expect(doc).toContain('## Flows, step by step');
  expect(doc).toContain('## Underlying API calls');
  expect(doc).toContain('GET /v1/decisions → 200');
  expect(doc).toContain('/openapi.json');
  expect(doc).toContain('## Current content');
});

test('the header copy-for-AI button exports the page in one click', async ({ page }) => {
  await page.goto('/decisions');
  await expect(page.getByRole('heading', { name: 'Decisions' })).toBeVisible();

  await page.getByTestId('ai-copy-trigger').click();
  await expect(page.getByText('Copied for AI')).toBeVisible();

  const doc = await page.evaluate(() => navigator.clipboard.readText());
  expect(doc).toContain('## Underlying API calls');
  expect(doc).toContain('GET /v1/decisions → 200');
});
