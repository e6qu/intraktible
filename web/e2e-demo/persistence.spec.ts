// SPDX-License-Identifier: AGPL-3.0-or-later
// Writes in the demo are real: each mutation appends to an event-log delta held in
// localStorage and replayed on boot, so a created/changed thing survives a full
// reload — and the Reset control drops the delta and restores the seed. This suite
// proves both, across several mutation kinds, so "the demo is read-only" can never
// creep back in.
import { test, expect, type Page } from '@playwright/test';
import { forcePersona, gotoReady } from './helpers';

// A short unique token so a created row is unambiguous and can't collide with the
// seed or a parallel worker.
function token(prefix: string): string {
  return `${prefix}-${Math.random().toString(36).slice(2, 8)}`;
}

// The demo boots as Ava (admin), so every authoring action below is permitted.

test('a created flow survives a full reload', async ({ page }) => {
  await forcePersona(page, 'builder');
  await gotoReady(page, 'engine');
  const slug = token('persist-flow');
  await page.getByLabel('slug', { exact: true }).fill(slug);
  await page.getByLabel('name', { exact: true }).fill('Persistence Flow');
  await page.getByRole('button', { name: 'Create flow' }).click();
  // Creating navigates into the new flow's builder once the backend acks.
  await expect(page).toHaveURL(/\/engine\/[^/]+$/);

  await gotoReady(page, 'engine');
  await expect(page.getByRole('cell', { name: slug })).toBeVisible();
  await page.reload();
  await expect(page.getByRole('cell', { name: slug })).toBeVisible();
});

test('a defined agent survives a full reload', async ({ page }) => {
  await forcePersona(page, 'developer');
  await gotoReady(page, 'agents');
  // The define form is a disclosure, collapsed while the seed already has agents.
  await page.getByText('+ Define agent', { exact: true }).click();
  const name = token('persist-agent');
  await page.getByLabel('agent name').fill(name);
  await page.getByLabel('system prompt').fill('Assess the applicant and return a verdict.');
  await page.getByRole('button', { name: 'Define agent', exact: true }).click();
  await expect(page.getByRole('link', { name })).toBeVisible();

  await page.reload();
  await expect(page.getByRole('link', { name })).toBeVisible();
});

test('a defined model survives a full reload', async ({ page }) => {
  await forcePersona(page, 'product');
  await gotoReady(page, 'models');
  const name = token('persist-model');
  await page.getByLabel('model name').fill(name);
  // A starter chip fills a valid spec for the chosen kind.
  await page.getByRole('button', { name: 'logistic', exact: true }).click();
  await page.getByRole('button', { name: 'Define model', exact: true }).click();
  await expect(page.getByRole('cell', { name }).first()).toBeVisible();

  await page.reload();
  await expect(page.getByRole('cell', { name }).first()).toBeVisible();
});

// A change to an existing seeded thing persists too — not just a fresh create.
test('a case status change survives a full reload', async ({ page }) => {
  await forcePersona(page, 'operator');
  await gotoReady(page, 'cases');
  // Open the first in_progress case (deterministic: the queue has 11 of them).
  await page.getByLabel('status filter').selectOption('in_progress');
  const firstCompany = page.locator('tbody tr td a[href*="/cases/"]').first();
  await expect(firstCompany).toBeVisible();
  const caseHref = await firstCompany.getAttribute('href');
  await firstCompany.click();
  await expect(page.getByTestId('case-status')).toBeVisible();

  await page.getByLabel('set status').selectOption('completed');
  await page.getByRole('button', { name: 'Set status' }).click();
  await expect(page.getByTestId('case-status')).toContainText('completed');

  await page.reload();
  await expect(page.getByTestId('case-status')).toContainText('completed');
  expect(caseHref).toBeTruthy();
});

// A posted comment (discussion) persists — the collaboration surface is real state,
// not ephemeral UI.
test('a posted comment survives a full reload', async ({ page }) => {
  await forcePersona(page, 'operator');
  await gotoReady(page, 'cases');
  await page.locator('tbody tr td a[href*="/cases/"]').first().click();
  const thread = page.getByTestId('comment-thread');
  await expect(thread).toBeVisible();
  const body = token('persist comment');
  await thread.getByLabel('new comment').fill(body);
  await thread.getByTestId('post-comment').click();
  await expect(thread.getByText(body)).toBeVisible();

  await page.reload();
  await expect(page.getByTestId('comment-thread').getByText(body)).toBeVisible();
});

// Reset drops the whole delta and reboots on the seed: a thing created this session
// is gone, and the seed is back.
test('Reset discards the session delta and restores the seed', async ({ page }) => {
  page.on('dialog', (d) => d.accept()); // Reset asks to confirm
  await forcePersona(page, 'builder');
  await gotoReady(page, 'engine');

  const slug = token('reset-flow');
  await page.getByLabel('slug', { exact: true }).fill(slug);
  await page.getByLabel('name', { exact: true }).fill('Reset Flow');
  await page.getByRole('button', { name: 'Create flow' }).click();
  await expect(page).toHaveURL(/\/engine\/[^/]+$/);
  await gotoReady(page, 'engine');
  await expect(page.getByRole('cell', { name: slug })).toBeVisible();

  await page.getByRole('button', { name: 'Reset' }).click();
  // The reboot lands on the seeded workspace: the created flow is gone, a seed flow
  // is present.
  await gotoReady(page, 'engine');
  await expect(page.getByRole('cell', { name: slug })).toHaveCount(0);
  await expect(page.getByRole('cell', { name: 'credit-decision' })).toBeVisible();
});

// The delta is per-browser-profile: a fresh context (no localStorage) boots on the
// pristine seed, with none of another session's writes.
test('a fresh browser profile boots on the pristine seed', async ({ browser }) => {
  const ctx = await browser.newContext();
  const page: Page = await ctx.newPage();
  await forcePersona(page, 'builder');
  await gotoReady(page, 'engine');
  // Exactly the 10 seeded flows, none of this suite's created rows.
  await expect(page.getByRole('cell', { name: 'credit-decision' })).toBeVisible();
  await expect(page.getByRole('cell', { name: /^persist-flow-/ })).toHaveCount(0);
  await expect(page.getByRole('cell', { name: /^reset-flow-/ })).toHaveCount(0);
  await ctx.close();
});
