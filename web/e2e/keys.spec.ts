// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('creates, reveals, rotates, and revokes an API key', async ({ page }) => {
  page.on('dialog', (d) => d.accept()); // revoking a key now asks to confirm
  await page.goto('/keys');
  await expect(page.getByRole('heading', { name: 'API keys' })).toBeVisible();

  const name = 'k-' + Math.random().toString(36).slice(2, 8);
  await page.getByLabel('key name').fill(name);
  await page.getByLabel('key actor').fill('svc-ci');
  await page.getByLabel('key role').selectOption('operator');
  await page.getByLabel('key scope').selectOption('sandbox');
  await page.getByRole('button', { name: 'Create key' }).click();

  // The secret is revealed exactly once.
  const secret = page.getByTestId('revealed-secret');
  await expect(secret).toBeVisible();
  await expect(secret).toContainText("won't be shown again");

  const row = page.locator('tbody tr').filter({ hasText: name });
  await expect(row).toBeVisible();
  await expect(row.getByText('active', { exact: true })).toBeVisible();

  // Rotate mints a fresh secret (revealed again).
  await row.getByRole('button', { name: 'Rotate' }).click();
  await expect(page.getByTestId('revealed-secret')).toBeVisible();

  // Revoke flips the key to a terminal status with no further actions.
  await row.getByRole('button', { name: 'Revoke' }).click();
  await expect(row.getByText('revoked', { exact: true })).toBeVisible();
});
