// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

// The compliance page is the officer's home surface — the adverse-action 30-day
// queue, the Article 22 human-review trail, lawful basis, and the admin-only
// governance card. It had no browser spec at all, which mattered more here than on
// most pages: this is the page whose sections were silently rendering empty on any
// failed read until that was fixed, and an empty compliance register reads as
// "nothing outstanding" rather than "could not load".
test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('renders every compliance section with its register export', async ({ page }) => {
  await page.goto('/compliance');
  await expect(page.getByRole('heading', { name: 'Compliance', level: 1 })).toBeVisible();

  // The four cards a compliance officer works from.
  await expect(page.getByRole('heading', { name: /Adverse-action queue/i })).toBeVisible();
  await expect(page.getByRole('heading', { name: /Human-review audit/i })).toBeVisible();
  await expect(page.getByRole('heading', { name: /Lawful basis/i })).toBeVisible();

  // The dev key is admin, so the governance card is present.
  await expect(page.getByRole('heading', { name: /Data governance/i })).toBeVisible();

  // Each register downloads as a real file rather than navigating away.
  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('button', { name: 'Export register ↓' }).first().click()
  ]);
  expect(download.suggestedFilename()).toContain('register');
});

// The governance card is admin-gated in the UI because the reads behind it are
// admin-gated in the API. Asserting the two agree is browser work: a mismatch shows
// up as a card that renders and then sits empty, which no API test can see.
//
// The contest -> review journey itself is covered at the API-e2e layer
// (reconsideration/service_e2e_test.go), where a declined, solely-automated decision
// can be set up directly. Reproducing that setup here would need a bound policy to
// produce the decline, and a browser test that guards its assertions behind whether
// the setup succeeded is worse than no test — it reads as coverage without being any.
test('gates the governance card on the admin role', async ({ page }) => {
  await page.goto('/compliance');
  await expect(page.getByRole('heading', { name: 'Compliance', level: 1 })).toBeVisible();

  // The dev key is admin, so the card renders AND its admin-only reads populated it.
  await expect(page.getByRole('heading', { name: /Data governance/i })).toBeVisible();
  await expect(page.getByText(/Retention/i).first()).toBeVisible();

  // The KPI row summarises the same registers the cards list, so a count that never
  // renders means a read resolved to nothing without anyone noticing.
  for (const label of ['Notices pending', 'Human reviews', 'Active lawful basis']) {
    await expect(page.getByText(label, { exact: true })).toBeVisible();
  }
});

test('a failed read reports itself instead of rendering an empty register', async ({ page }) => {
  // An empty compliance register and a failed one look identical to a reader, and
  // only one of them means "nothing outstanding". Fail the read and require the page
  // to say so.
  await page.route('**/v1/adverse-actions**', (route) =>
    route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"boom"}' })
  );

  await page.goto('/compliance');

  await expect(page.getByText(/could not be loaded/i)).toBeVisible();
  // And it must not present the registers as though they were read successfully.
  await expect(page.getByRole('heading', { name: /Adverse-action queue/i })).toHaveCount(0);
});
