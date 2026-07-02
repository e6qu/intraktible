// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('the notifications bell mounts and opens the inbox', async ({ page }) => {
  await page.goto('/');
  const bell = page.getByTestId('notifications-bell');
  await expect(bell).toBeVisible();
  await bell.locator('summary').click();
  // The inbox panel opens. We don't assert it's empty: case events (human-review tasks)
  // and @-mentions both feed it, so depending on what else this run created it may hold
  // items or show the caught-up state — either is valid here. The per-source behaviour is
  // covered by the Go unit tests (notifications.TestTaskNotificationsFromCaseLifecycle).
  await expect(bell.getByText('Notifications')).toBeVisible();
});

test('Escape and outside clicks dismiss the dropdown', async ({ page }) => {
  await page.goto('/');
  const bell = page.getByTestId('notifications-bell');
  const panel = bell.locator('.panel');
  await bell.locator('summary').click();
  await expect(panel).toBeVisible();

  // Escape closes while focus is still on the summary (the click leaves it there).
  await page.keyboard.press('Escape');
  await expect(panel).toBeHidden();

  // Reopen, then a click outside the dropdown dismisses it.
  await bell.locator('summary').click();
  await expect(panel).toBeVisible();
  await page.locator('body').click({ position: { x: 5, y: 400 } });
  await expect(panel).toBeHidden();
});
