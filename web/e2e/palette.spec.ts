// SPDX-License-Identifier: AGPL-3.0-or-later
// The ⌘K command palette: keyboard-open, filter, navigate, switch persona.
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('opens with the keyboard, filters, and navigates', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('cmdk-trigger')).toBeVisible(); // wait for hydration
  await page.keyboard.press('ControlOrMeta+k');
  const search = page.getByRole('combobox', { name: 'Search commands' });
  await expect(search).toBeVisible();
  await search.fill('audit');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/\/audit$/);
  await expect(page.getByRole('heading', { name: /Audit log/i })).toBeVisible();
});

test('the header trigger opens it and Escape closes it', async ({ page }) => {
  await page.goto('/');
  await page.getByTestId('cmdk-trigger').click();
  const search = page.getByRole('combobox', { name: 'Search commands' });
  await expect(search).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(search).not.toBeVisible();
});

test('switches the view persona', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('cmdk-trigger')).toBeVisible(); // wait for hydration
  await page.keyboard.press('ControlOrMeta+k');
  await page.getByRole('combobox', { name: 'Search commands' }).fill('view as operator');
  await page.keyboard.press('Enter');
  await expect(page.locator('html')).toHaveAttribute('data-persona', 'operator');
});
