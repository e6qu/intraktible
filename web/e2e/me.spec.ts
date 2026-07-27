// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

// The account page is what an operator checks to confirm WHO they are acting as
// before doing something consequential, so showing a stale or wrong identity is the
// failure that matters — as is failing to distinguish signed-in from signed-out.

test('shows the authenticated identity', async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });

  await page.goto('/me');
  await expect(page.getByRole('heading', { name: /Signed in as/i })).toBeVisible();
  await expect(page.getByRole('region', { name: /Current account details/i })).toBeVisible();
  await expect(page.getByRole('heading', { name: /You are not signed in/i })).toHaveCount(0);
});

test('sends a signed-out visitor to sign in rather than showing an account', async ({ page }) => {
  // A fresh context carries no session cookie. The account page must not render an
  // identity it does not have — it routes to the sign-in surface instead.
  await page.goto('/me');
  await expect(page.getByRole('heading', { name: /Signed in as/i })).toHaveCount(0);
  await expect(page.getByText(/Exchange an API key for a session/i)).toBeVisible();
});
