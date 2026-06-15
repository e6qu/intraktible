// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

// Covers the Phase 0 hello slice on the landing page. The UI authenticates via the
// session cookie now, so the demo tests sign in first; the error test does not.
async function signIn(page: import('@playwright/test').Page) {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
}

test('landing page renders', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { name: /intraktible/i })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Say hello' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Refresh' })).toBeVisible();
});

test('say hello posts a greeting and refreshes stats', async ({ page }) => {
  await signIn(page);
  await page.goto('/');
  await page.getByLabel('name').fill('playwright');
  await page.getByRole('button', { name: 'Say hello' }).click();

  // say() posts then overwrites the output with refreshed stats; the greeting
  // must appear there. Refresh until the eventually-consistent projection shows it.
  const output = page.locator('pre');
  await expect(async () => {
    await page.getByRole('button', { name: 'Refresh' }).click();
    await expect(output).toContainText('"last_name": "playwright"');
    await expect(output).toContainText('"count"');
  }).toPass({ timeout: 5000 });
});

test('refresh shows current stats', async ({ page }) => {
  await signIn(page);
  await page.goto('/');
  await page.getByRole('button', { name: 'Refresh' }).click();
  await expect(page.locator('pre')).toContainText('"count"');
});

test('an unauthenticated request surfaces an error, not silent success', async ({ page }) => {
  // No sign-in: the session cookie is absent, so the call must fail loudly (401)
  // and the UI must display the error (not a fake success).
  await page.goto('/');
  await page.getByRole('button', { name: 'Refresh' }).click();
  const output = page.locator('pre');
  await expect(output).toContainText('Error:');
  await expect(output).toContainText('401');
  await expect(output).not.toContainText('"count"');
});
