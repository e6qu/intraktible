// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';
const uniqueSlug = () => 'ui-' + Math.random().toString(36).slice(2, 9);

// The dev key resolves to the admin role, which the audit surface requires.
test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('shows the event-log audit trail and filters it', async ({ page, request }) => {
  // Creating a flow appends a decision.flow.created event to the log.
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Audited' }
  });
  expect(created.ok()).toBeTruthy();

  await page.goto('/audit');
  await expect(page.getByRole('heading', { name: /Audit log/i })).toBeVisible();

  // The flow-created event surfaces with its actor + stream (retries while the
  // request round-trips and the client fetches).
  const rows = page.locator('tbody tr');
  await expect(rows.filter({ hasText: 'decision.flow.created' }).first()).toBeVisible();
  await expect(rows.filter({ hasText: 'dev' }).first()).toBeVisible();

  // Filtering to a stream that produced no events empties the table.
  await page.getByLabel('stream filter').fill('does-not-exist');
  await page.getByRole('button', { name: 'Apply' }).click();
  await expect(page.getByText('No matching audit events.')).toBeVisible();

  // The CSV export link carries the active filter.
  await expect(page.getByTestId('audit-csv')).toHaveAttribute('href', /format=csv/);
});
