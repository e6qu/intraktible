// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

// Covers the Phase 0 hello slice, now on its own /hello route (the landing page is
// a persona-aware dashboard). The UI authenticates via the session cookie now, so
// the demo tests sign in first; the error test does not.
async function signIn(page: import('@playwright/test').Page) {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
}

test('landing page renders with the persona switcher', async ({ page }) => {
  await page.goto('/');
  // The landing is a persona-aware dashboard; the persona switcher (a "view-as"
  // control available to everyone) is the one element common to every persona.
  await expect(page.getByTestId('persona-switch')).toBeVisible();
});

test('hello slice page renders', async ({ page }) => {
  await page.goto('/hello');
  await expect(page.getByRole('button', { name: 'Say hello' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Refresh' })).toBeVisible();
});

test('say hello posts a greeting and refreshes stats', async ({ page }) => {
  await signIn(page);
  await page.goto('/hello');
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
  await page.goto('/hello');
  await page.getByRole('button', { name: 'Refresh' }).click();
  await expect(page.locator('pre')).toContainText('"count"');
});

test('an unauthenticated request surfaces an error, not silent success', async ({ page }) => {
  // No sign-in: the session cookie is absent, so the call must fail loudly (401)
  // and the UI must display the error (not a fake success).
  await page.goto('/hello');
  await page.getByRole('button', { name: 'Refresh' }).click();
  const output = page.locator('pre');
  await expect(output).toContainText('Error:');
  // The client surfaces the server's explanation, not a bare status code.
  await expect(output).toContainText('authentication required');
  await expect(output).not.toContainText('"count"');
});

test('stats load on page open without a click', async ({ page }) => {
  await signIn(page);
  await page.goto('/hello');
  // No Refresh click: the page fetches stats on mount instead of sitting on a
  // "stats will appear here…" placeholder.
  await expect(page.locator('pre')).toContainText('"count"');
});
