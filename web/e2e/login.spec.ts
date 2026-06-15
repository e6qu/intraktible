// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

test('sign in with an API key, then sign out', async ({ page }) => {
  await page.goto('/login');
  await expect(page.getByRole('heading', { name: /Sign in/i })).toBeVisible();

  await page.getByLabel('API key').fill('dev-sandbox-key');
  await page.getByRole('button', { name: 'Sign in' }).click();

  // Redirected home; the session cookie now authenticates /v1/me.
  const status = page.getByTestId('auth-status');
  await expect(status).toContainText('Signed in as');
  await expect(status).toContainText('dev');

  await page.getByRole('button', { name: 'Sign out' }).click();
  await expect(status).toContainText('Not signed in');
});

test('a bad API key surfaces an error and does not sign in', async ({ page }) => {
  await page.goto('/login');
  await page.getByLabel('API key').fill('not-a-real-key');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByTestId('login-error')).toContainText('invalid api key');
});
