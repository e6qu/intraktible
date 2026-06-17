// SPDX-License-Identifier: AGPL-3.0-or-later
// Keyboard shortcuts: the ? overlay, theme toggle (t), and g-then-key navigation.
import { test, expect } from '@playwright/test';

// Wait for the layout to hydrate (its window keydown listener attaches on mount)
// before pressing keys. The header ⌘K trigger is the hydration signal.
async function ready(page: import('@playwright/test').Page) {
  await page.goto('/');
  await expect(page.getByTestId('cmdk-trigger')).toBeVisible();
}

test('? opens the shortcuts overlay and Esc closes it', async ({ page }) => {
  await ready(page);
  await page.keyboard.press('?');
  const dialog = page.getByRole('dialog', { name: 'Keyboard shortcuts' });
  await expect(dialog).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(dialog).not.toBeVisible();
});

test('t toggles the theme', async ({ page }) => {
  await ready(page);
  const html = page.locator('html');
  const before = await html.getAttribute('data-theme');
  await page.keyboard.press('t');
  await expect(html).toHaveAttribute('data-theme', before === 'dark' ? 'light' : 'dark');
});

test('g then e jumps to the engine', async ({ page }) => {
  await ready(page);
  await page.keyboard.press('g');
  await page.keyboard.press('e');
  await expect(page).toHaveURL('/engine');
});

test('the command palette can open the shortcuts overlay', async ({ page }) => {
  await ready(page);
  await page.keyboard.press('ControlOrMeta+k');
  await page.getByRole('combobox', { name: 'Search commands' }).fill('shortcuts');
  await page.keyboard.press('Enter');
  await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible();
});
