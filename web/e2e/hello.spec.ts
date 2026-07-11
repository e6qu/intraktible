// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

// The Phase-0 backbone (command → event log → projection → API → UI) lives on the
// /hello slice, which is dev scaffolding — not a product surface. It is kept only
// for the interactive wasm demo and is compiled out of the product build, where it
// redirects to the dashboard. Its client calls (getStats/sayHello, including the
// fail-fast 401/400 surfacing) are unit-tested in src/lib/api.test.ts; this suite
// covers the landing page and the production gate.

test('landing page renders with the persona switcher', async ({ page }) => {
  await page.goto('/');
  // The landing is a persona-aware dashboard; the persona switcher (a "view-as"
  // control available to everyone) is the one element common to every persona.
  await expect(page.getByTestId('persona-switch')).toBeVisible();
});

test('the hello scaffolding slice is excluded from the product build', async ({ page }) => {
  // A non-demo (product) build must not expose the backbone showcase to operators:
  // it redirects to the dashboard rather than rendering the Say hello / Refresh UI.
  await page.goto('/hello');
  // The redirect is a client-side replaceState on mount, so wait for the URL to
  // settle off /hello before asserting the scaffolding UI is absent.
  await page.waitForURL((url) => !url.pathname.replace(/\/$/, '').endsWith('/hello'));
  await expect(page.getByTestId('persona-switch')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Say hello' })).toHaveCount(0);
});
