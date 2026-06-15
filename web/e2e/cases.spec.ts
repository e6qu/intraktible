// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test('opens a case from the queue', async ({ page }) => {
  await page.goto('/cases');
  await expect(page.getByRole('heading', { name: /Case Manager/i })).toBeVisible();

  await page.getByLabel('company name').fill('Acme UI');
  await page.getByLabel('case type').fill('aml');
  await page.getByLabel('sla days').fill('5');
  await page.getByRole('button', { name: 'Open case' }).click();

  await expect(page.getByRole('link', { name: 'Acme UI' })).toBeVisible();
});

test('assigns, transitions, and notes a case', async ({ page, request }) => {
  // Seed a case through the API.
  const created = await request.post('/v1/cases', {
    headers: { 'X-Api-Key': KEY },
    data: { company_name: 'Globex UI', case_type: 'kyb_kyc', sla_days: 3 }
  });
  expect(created.ok()).toBeTruthy();
  const { case_id } = await created.json();

  await page.goto(`/cases/${case_id}`);

  await page.getByLabel('assignee').fill('adam');
  await page.getByRole('button', { name: 'Assign' }).click();
  await page.getByLabel('set status').selectOption('in_progress');
  await page.getByRole('button', { name: 'Set status' }).click();
  await page.getByLabel('note', { exact: true }).fill('reviewed the docs');
  await page.getByRole('button', { name: 'Add note' }).click();

  // The read model is eventually consistent; reload until it reflects all three
  // actions (audit: requested + assigned + status_changed + note_added = 4).
  await expect(async () => {
    await page.getByRole('button', { name: 'Reload' }).click();
    await expect(page.getByTestId('case-status')).toHaveText('in_progress');
    await expect(page.getByTestId('audit').locator('li')).toHaveCount(4);
  }).toPass({ timeout: 5000 });
});
