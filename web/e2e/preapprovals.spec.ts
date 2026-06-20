// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('grants a pre-approval and revokes it', async ({ page }) => {
  const eid = 'acme-' + Math.random().toString(36).slice(2, 8);

  await page.goto('/preapprovals');
  await expect(page.getByRole('heading', { name: 'Pre-approvals' })).toBeVisible();

  await page.getByLabel('Entity type').fill('applicant');
  await page.getByLabel('Entity ID').fill(eid);
  await page.getByLabel('Disposition').selectOption('approve');
  await page.getByRole('button', { name: /Grant pre-approval/ }).click();

  // The new pre-approval appears in the list as active/approve.
  const row = page.locator('tr', { hasText: eid });
  await expect(row).toBeVisible();
  await expect(row).toContainText('approve');
  await expect(row).toContainText('active');

  // Revoke it via the inline reason form (replaces the old native prompt).
  await row.getByRole('button', { name: 'Revoke' }).click();
  await page.getByLabel('revoke reason').fill('test cleanup');
  await page.getByRole('button', { name: 'Confirm revoke' }).click();
  await expect(page.locator('tr', { hasText: eid }).first()).toContainText('revoked');
});
