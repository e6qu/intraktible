// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

test('sign in with an API key, then sign out', async ({ page }) => {
  await page.goto('/login');
  await expect(page.getByRole('heading', { name: /Sign in/i })).toBeVisible();
  // Minimal chrome on the sign-in screen: no primary nav or account control.
  await expect(page.getByRole('navigation', { name: 'Primary' })).toHaveCount(0);
  await expect(page.getByTestId('persona-switch')).toHaveCount(0);

  await page.getByLabel('API key').fill('dev-sandbox-key');
  await page.getByRole('button', { name: 'Sign in' }).click();

  // Redirected home; the full chrome returns and identity + sign-out live in the
  // account & view menu.
  await expect(page.getByRole('navigation', { name: 'Primary' })).toBeVisible();
  await page.getByTestId('persona-switch').locator('summary').click();
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
