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
  await expect(page.getByText('No matching audit events')).toBeVisible();

  // The active filter lives in the URL — the view is deep-linkable / shareable.
  await expect(page).toHaveURL(/[?&]stream=does-not-exist/);

  // The CSV export downloads the filtered log (a Blob, so it works through the demo's
  // fetch mock — an <a href="/v1/…"> would escape it).
  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.getByTestId('audit-csv').click()
  ]);
  expect(download.suggestedFilename()).toBe('audit.csv');

  // Navigating straight to a filtered URL restores the inputs and the view.
  await page.goto('/audit?type=decision.flow.created');
  await expect(page.getByLabel('type filter')).toHaveValue('decision.flow.created');
  await expect(rows.filter({ hasText: 'decision.flow.created' }).first()).toBeVisible();
});

test('creates and revokes a managed API token (admin)', async ({ page }) => {
  await page.goto('/audit');
  const panel = page.getByTestId('api-keys-config');
  await panel.getByText('API tokens').click(); // open the <details>

  const name = 'tok-' + Math.random().toString(36).slice(2, 7);
  await panel.getByLabel('token name').fill(name);
  await panel.getByLabel('token actor').fill('ci@acme');
  await panel.getByLabel('token role').selectOption('editor');
  await panel.getByTestId('create-token').click();

  // The generated secret is revealed exactly once, right after creation.
  await expect(panel.getByTestId('new-secret')).toContainText('itk_');

  // The new token shows in the table as active.
  const row = panel.locator('tbody tr', { hasText: name });
  await expect(row.getByText('active')).toBeVisible();

  // Rotating mints a fresh secret (shown once) and notes the grace window; the
  // token stays active.
  await row.getByRole('button', { name: 'Rotate' }).click();
  await expect(panel.getByTestId('new-secret')).toContainText('itk_');
  await expect(panel.getByTestId('new-secret')).toContainText('previous secret keeps working');
  await expect(row.getByText('active')).toBeVisible();

  // Revoking flips its status.
  await row.getByRole('button', { name: 'Revoke' }).click();
  await expect(row.getByText('revoked')).toBeVisible();

  // The per-token Audit link deep-links to that token's trail — create, rotate,
  // and revoke each left an event-log breadcrumb attributed to the admin.
  await row.getByRole('link', { name: 'Audit' }).click();
  const rows = page.locator('tbody tr');
  await expect(rows.filter({ hasText: 'auth.managed_key.created' }).first()).toBeVisible();
  await expect(rows.filter({ hasText: 'auth.managed_key.rotated' }).first()).toBeVisible();
  await expect(rows.filter({ hasText: 'auth.managed_key.revoked' }).first()).toBeVisible();
});

test('configures PII masking fields (admin)', async ({ page }) => {
  await page.goto('/audit');
  const panel = page.getByTestId('masking-config');
  await panel.getByText('PII masking').click(); // open the <details>
  await panel.getByLabel('masked fields').fill('ssn, email');
  await panel.getByTestId('save-masking').click();

  // The config round-trips: reloading rehydrates the saved fields from the server.
  await page.reload();
  await page.getByTestId('masking-config').getByText('PII masking').click();
  await expect(page.getByTestId('masking-config').getByLabel('masked fields')).toHaveValue(/ssn/);
});

test('Reload fetches the applied filter, not draft inputs', async ({ page, request }) => {
  // Ensure at least one event exists (creating a flow appends one).
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug: uniqueSlug(), name: 'Audit Draft' }
  });
  expect(created.ok()).toBeTruthy();

  await page.goto('/audit');
  const rows = page.locator('tbody tr');
  const flowRow = rows.filter({ hasText: 'decision.flow.created' }).first();
  await expect(flowRow).toBeVisible();

  // Edit a filter input WITHOUT applying it — Reload must keep showing the
  // applied (URL) filter's rows, which are what the CSV export covers.
  await page.getByLabel('actor filter').fill('actor-that-matches-nothing');
  await Promise.all([
    page.waitForResponse((r) => r.url().includes('/v1/audit')),
    page.getByRole('button', { name: 'Reload' }).click()
  ]);
  await expect(flowRow).toBeVisible();

  // Apply does take effect.
  await page.getByRole('button', { name: 'Apply' }).click();
  await expect(page.getByText('No matching audit events')).toBeVisible();
});
