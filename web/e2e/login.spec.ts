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
  await expect(page.getByTestId('user-identity')).toHaveText('dev');
  await expect(page.getByTestId('user-avatar')).toHaveText('D');
  await page.getByTestId('persona-switch').locator('summary').click();
  const status = page.getByTestId('auth-status');
  await expect(status).toContainText('Signed in as');
  await expect(status).toContainText('dev');

  await page.getByRole('link', { name: 'My account' }).click();
  await expect(page.getByRole('heading', { name: 'Signed in as dev' })).toBeVisible();
  await expect(page.getByText('Organization')).toBeVisible();
  await expect(page.getByText('Workspace')).toBeVisible();

  // Signing out returns to the sign-in screen (and drops the full chrome), so the
  // signed-out state is unambiguous rather than a stripped-down dashboard.
  await page
    .getByLabel('Current account details')
    .getByRole('button', { name: 'Sign out' })
    .click();
  await expect(page.getByRole('heading', { name: /Sign in/i })).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Primary' })).toHaveCount(0);
});

test('a bad API key surfaces an error and does not sign in', async ({ page }) => {
  await page.goto('/login');
  await page.getByLabel('API key').fill('not-a-real-key');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByTestId('login-error')).toContainText('invalid api key');
});
