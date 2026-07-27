// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

// The disparate-impact screen breaks a flow's whole decision population down by a
// protected-class attribute, so it is admin-only. Both halves of that matter: the
// report renders for an admin, and a refusal renders as an explained restriction
// rather than a broken or — worse — an empty-looking report, which on this page
// would read as "no disparate impact found".
test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('renders the disparate-impact screen for an admin', async ({ page }) => {
  await page.goto('/fairlending');
  await expect(page.getByRole('heading', { name: 'Fair lending', level: 1 })).toBeVisible();

  // The page states its own limits: the attribute is chosen, never inferred, and the
  // four-fifths ratio is a screen rather than a legal conclusion. That framing is
  // load-bearing for a compliance surface, so it is asserted rather than assumed.
  await expect(page.getByText(/does not infer one/i)).toBeVisible();

  await expect(page.getByRole('combobox').first()).toBeVisible();
  await expect(page.getByText(/Restricted to the admin role/i)).toHaveCount(0);
});

test('explains a refusal instead of showing an empty report', async ({ page }) => {
  await page.route('**/v1/fairlending/report**', (route) =>
    route.fulfill({ status: 403, contentType: 'application/json', body: '{"error":"forbidden"}' })
  );
  await page.route('**/v1/flows**', (route) =>
    route.fulfill({ status: 403, contentType: 'application/json', body: '{"error":"forbidden"}' })
  );

  await page.goto('/fairlending');
  await expect(page.getByText(/Restricted to the admin role/i)).toBeVisible();
});
