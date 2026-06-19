// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

// The UI authenticates via the session cookie; sign the page context in first.
test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('opens a case from the queue and shows SLA + summary', async ({ page }) => {
  await page.goto('/cases');
  await expect(page.getByRole('heading', { name: /Case Manager/i })).toBeVisible();

  await page.getByLabel('company name').fill('Acme UI');
  await page.getByLabel('case type').fill('aml');
  await page.getByLabel('sla days').fill('5');
  await page.getByRole('button', { name: 'Open case' }).click();

  // .first(): a reused dev server may carry "Acme UI" cases from prior runs.
  await expect(page.getByRole('link', { name: 'Acme UI' }).first()).toBeVisible();
  // The queue summary banner reflects the open case(s).
  const summary = page.getByLabel('queue summary');
  await expect(summary).toContainText('Total');
  await expect(summary).toContainText('Overdue');

  // The SLA sweep runs without error (a fresh 5-day case is not overdue).
  await page.getByRole('button', { name: 'Run SLA sweep' }).click();
  await expect(page.locator('p.err')).toHaveCount(0);
});

test('bulk-assigns selected cases from the queue', async ({ page, request }) => {
  const tag = 'bulk-' + Math.random().toString(36).slice(2, 7);
  for (const n of [1, 2]) {
    await request.post('/v1/cases', {
      headers: { 'X-Api-Key': KEY },
      data: { company_name: `${tag}-${n}`, case_type: 'aml', sla_days: 5 }
    });
  }
  await page.goto('/cases');

  // Select the two cases this test created via their row checkboxes.
  for (const n of [1, 2]) {
    await page.getByRole('checkbox', { name: `select ${tag}-${n}` }).check();
  }
  const bar = page.getByTestId('bulk-bar');
  await expect(bar).toContainText('2 selected');
  await bar.getByLabel('bulk assignee').fill('reviewer@x');
  await bar.getByRole('button', { name: 'Assign' }).click();

  // Both rows now show the assignee; the bulk bar clears after the action.
  for (const n of [1, 2]) {
    const row = page.locator('tbody tr').filter({ hasText: `${tag}-${n}` });
    await expect(row).toContainText('reviewer@x');
  }
  await expect(page.getByTestId('bulk-bar')).toHaveCount(0);
});

test('case detail shows computed days-left', async ({ page, request }) => {
  // A freshly opened 5-day case is on track with ~5 days left.
  const created = await request.post('/v1/cases', {
    headers: { 'X-Api-Key': KEY },
    data: { company_name: 'Initech UI', case_type: 'aml', sla_days: 5 }
  });
  expect(created.ok()).toBeTruthy();
  const { case_id } = await created.json();

  await page.goto(`/cases/${case_id}`);
  const daysLeft = page.getByTestId('days-left');
  await expect(daysLeft).toContainText('on_track');
  await expect(daysLeft).toContainText(/[45]/);
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

test('case detail renders the context as a key-value view', async ({ page, request }) => {
  // Seed a case with context (as a decision/agent escalation would carry).
  const created = await request.post('/v1/cases', {
    headers: { 'X-Api-Key': KEY },
    data: {
      company_name: 'Context Co',
      case_type: 'aml',
      sla_days: 5,
      context: { subject: 'Acme Corp', fico: 700 }
    }
  });
  expect(created.ok()).toBeTruthy();
  const { case_id } = await created.json();

  await page.goto(`/cases/${case_id}`);
  const ctx = page.getByTestId('context');
  await expect(ctx).toContainText('subject');
  await expect(ctx).toContainText('Acme Corp');
  await expect(ctx).toContainText('fico');
});
