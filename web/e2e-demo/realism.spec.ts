// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect, type Page } from '@playwright/test';

// Realism regressions from the demo deep-audit: interactions a prospect tries
// must behave like the real product, not silently snap back or hide evidence.

// The seed's ids are minted by the real backend (random per regeneration), so a
// detail page is reached the way a user reaches it: from the flow list.
async function openFlow(page: Page, slug: string): Promise<void> {
  await page.goto('engine');
  const row = page.locator('tbody tr').filter({ hasText: slug });
  await row.first().locator('a[href*="/engine/"]').first().click();
  await expect(page.locator('h1, h2').first()).toBeVisible();
}

// The operator lens presets the queue to needs_review; choosing "all" must widen
// the view instead of being re-overridden by the lens on navigation.
test("operator: the 'all' cases filter overrides the persona lens", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('intraktible-persona', 'operator'));
  await page.goto('cases');
  const filter = page.getByLabel('status filter');
  await expect(filter).toHaveValue('needs_review');
  const rows = page.locator('tbody tr');
  const preset = await rows.count();

  await filter.selectOption('');
  await expect(filter).toHaveValue('');
  await expect
    .poll(async () => rows.count(), { message: 'all statuses should widen the queue' })
    .toBeGreaterThan(preset);
  await expect(page.locator('tbody')).toContainText('completed');
});

// The seeded suspended decisions are reachable through the status filter, not only
// by knowing their ids.
test('decisions: the suspended status filter finds the paused decision', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('intraktible-persona', 'builder'));
  await page.goto('decisions');
  await page.getByLabel('filter by status').selectOption('suspended');
  await page.getByRole('button', { name: 'Apply' }).click();
  const rows = page.locator('tbody tr');
  await expect(rows.first()).toBeVisible();
  await expect(page.locator('tbody').getByText('suspended').first()).toBeVisible();
});

// A pre-approval-honored test run announces itself on the builder verdict card:
// the grant badge and the "honored · flow skipped" evidence, not an
// indistinguishable input echo. (The real honored fast path returns the grant id
// and disposition reason; it does not synthesize a reason code — that was an
// embellishment of the retired TS mock.)
test('builder verdict card surfaces a honored pre-approval', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('intraktible-persona', 'builder'));
  await openFlow(page, 'credit-decision');
  // Grant through the page's own fetch so the embedded backend records it (the
  // session cookie authenticates; the CSRF header is required on mutations).
  await page.evaluate(async () => {
    const res = await fetch('/v1/preapprovals', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'intraktible' },
      body: JSON.stringify({
        entity_type: 'applicant',
        entity_id: 'e2e-honored',
        disposition: 'approve',
        terms: { limit: 9000 },
        valid_days: 30,
        flow_slug: 'credit-decision'
      })
    });
    if (!res.ok) throw new Error(`grant failed: ${res.status} ${await res.text()}`);
  });

  await page.getByRole('button', { name: 'Sample input' }).click();
  await page.getByPlaceholder('entity type (optional)').fill('applicant');
  await page.getByPlaceholder('entity id (optional)').fill('e2e-honored');
  await page.getByRole('button', { name: 'Run', exact: true }).click();

  const verdict = page.getByTestId('run-verdict');
  await expect(verdict).toBeVisible();
  await expect(verdict.getByText('pre-approved')).toBeVisible();
  await expect(verdict.getByText('honored · flow skipped')).toBeVisible();
});

// Four-eyes approval is an in-app interaction: the reason is collected inline in
// the request row (no native prompt()), and the decision + note persist on it.
test('four-eyes approval collects its reason inline', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('intraktible-persona', 'builder'));
  await openFlow(page, 'credit-decision');
  await page.getByRole('button', { name: 'Deploy & versions' }).click();

  const requests = page.getByTestId('deployment-requests');
  await expect(requests).toBeVisible();
  const pending = requests.locator('tbody tr:not(.threadrow)').filter({ hasText: 'pending' });
  await expect(pending.first()).toBeVisible();

  // The seeded request was proposed by Priya; Ava (admin) satisfies four-eyes.
  await pending.first().getByRole('button', { name: 'Approve' }).click();
  const reason = requests.getByLabel('decision reason');
  await expect(reason).toBeVisible();
  await reason.fill('Backtest green; shipping.');
  await requests.getByRole('button', { name: 'Confirm approve' }).click();

  await expect(requests.getByText('approved by', { exact: false }).first()).toBeVisible();
  await expect(requests.getByText('Backtest green; shipping.').first()).toBeVisible();
});
