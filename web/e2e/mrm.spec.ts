// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

// The SR 11-7 / SS1/23 inventory is the artifact that asserts governance was
// checked, so "renders nothing" and "could not be read" must not look alike here.
test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('renders the model inventory for an admin', async ({ page }) => {
  await page.goto('/mrm');
  await expect(page.getByRole('heading', { name: 'Model risk', level: 1 })).toBeVisible();
  await expect(page.getByText(/SR 11-7/i)).toBeVisible();
  await expect(page.getByText(/Restricted to the admin role/i)).toHaveCount(0);
});

// The report body must render whether or not the workspace has models yet. It did
// not: the API encodes an empty inventory as `"models": null`, the page read
// report.models.length, and the resulting TypeError blanked everything below the
// lede — a governance surface showing nothing at all, with no error to explain it.
test('renders the inventory body and exports it, even with no models yet', async ({ page }) => {
  await page.goto('/mrm');
  await expect(page.getByRole('heading', { name: 'Model risk', level: 1 })).toBeVisible();

  // The summary row renders — this is the assertion that fails if the body throws.
  await expect(page.getByLabel('inventory summary')).toBeVisible();

  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('button', { name: /Export Markdown/i }).click()
  ]);
  expect(download.suggestedFilename()).toMatch(/\.md$/);
});

test('explains a refusal instead of showing an empty inventory', async ({ page }) => {
  await page.route('**/v1/mrm**', (route) =>
    route.fulfill({ status: 403, contentType: 'application/json', body: '{"error":"forbidden"}' })
  );

  await page.goto('/mrm');
  await expect(page.getByText(/Restricted to the admin role/i)).toBeVisible();
});
