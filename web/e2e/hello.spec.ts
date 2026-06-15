// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

// Covers every UI flow currently exposed by the app (the Phase 0 hello slice).
// As the Decision Engine builder UI lands, its flows get their own specs here.

test('landing page renders', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { name: /intraktible/i })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Say hello' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Refresh' })).toBeVisible();
});

test('say hello posts a greeting and refreshes stats', async ({ page }) => {
  await page.goto('/');
  await page.getByLabel('name').fill('playwright');
  await page.getByRole('button', { name: 'Say hello' }).click();

  // say() posts then overwrites the output with refreshed stats; the greeting
  // must appear there. Refresh until the eventually-consistent projection shows
  // it (robust under parallel load on the shared server).
  const output = page.locator('pre');
  await expect(async () => {
    await page.getByRole('button', { name: 'Refresh' }).click();
    await expect(output).toContainText('"last_name": "playwright"');
    await expect(output).toContainText('"count"');
  }).toPass({ timeout: 5000 });
});

test('refresh shows current stats', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('button', { name: 'Refresh' }).click();
  await expect(page.locator('pre')).toContainText('"count"');
});

test('a rejected api key surfaces an error, not silent success', async ({ page }) => {
  await page.goto('/');
  await page.getByLabel('API key').fill('not-a-valid-key');
  // The client fails loudly on non-2xx; the UI must display the error (not a
  // fake success and not an unhandled rejection).
  await page.getByRole('button', { name: 'Refresh' }).click();
  const output = page.locator('pre');
  await expect(output).toContainText('Error:');
  await expect(output).toContainText('401');
  await expect(output).not.toContainText('"count"');
});
