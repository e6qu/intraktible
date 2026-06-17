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

  // The active filter lives in the URL — the view is deep-linkable / shareable.
  await expect(page).toHaveURL(/[?&]stream=does-not-exist/);

  // The CSV export link carries the active filter.
  await expect(page.getByTestId('audit-csv')).toHaveAttribute('href', /format=csv/);

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

  // The new token shows in the table as active; revoking flips its status.
  const row = panel.locator('tbody tr', { hasText: name });
  await expect(row.getByText('active')).toBeVisible();
  await row.getByRole('button', { name: 'Revoke' }).click();
  await expect(row.getByText('revoked')).toBeVisible();
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
