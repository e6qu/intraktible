// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('the notifications bell mounts and shows the inbox', async ({ page }) => {
  await page.goto('/');
  const bell = page.getByTestId('notifications-bell');
  await expect(bell).toBeVisible();
  // No @-mentions for this user yet, so no unread badge.
  await expect(page.getByTestId('notif-badge')).toHaveCount(0);
  await bell.locator('summary').click();
  await expect(bell.getByText("You're all caught up.")).toBeVisible();
});
