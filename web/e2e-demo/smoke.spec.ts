// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect, type Page } from '@playwright/test';

// Every top-level route renders (the seeded in-browser backend populates them) with
// no uncaught page error and no visible error banner. Relative paths resolve under
// the /demo base (baseURL ends in /demo/).
const ROUTES = [
  '', // home dashboard
  'engine',
  'decisions',
  'cases',
  'agents',
  'models',
  'data',
  'policies',
  'preapprovals',
  'observability',
  'mrm',
  'keys',
  'audit'
];

// Fail loudly on an uncaught exception — that means the mock returned a shape the
// page could not consume (the failure mode this smoke exists to catch).
function trackPageErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));
  return errors;
}

for (const route of ROUTES) {
  test(`renders /${route} from the in-browser backend`, async ({ page }) => {
    const errors = trackPageErrors(page);
    await page.goto(route);
    // The signed-in shell (/v1/me is mocked) shows a heading on every page.
    await expect(page.locator('h1, h2').first()).toBeVisible();
    // No error banner surfaced from a failed/odd mock response.
    await expect(page.locator('p.err')).toHaveCount(0);
    expect(errors, `uncaught error(s) on /${route}: ${errors.join('; ')}`).toEqual([]);
  });
}

// Detail pages render too — navigate from the list so we use a real seeded id.
test('opens a flow, a decision, a case, and an agent from their lists', async ({ page }) => {
  const errors = trackPageErrors(page);

  await page.goto('engine');
  await page.locator('a[href*="/engine/"]').first().click();
  await expect(page.locator('h1, h2').first()).toBeVisible();

  await page.goto('decisions');
  const decisionLink = page.locator('a[href*="/decisions/"]').first();
  if (await decisionLink.count()) {
    await decisionLink.click();
    await expect(page.locator('h1, h2').first()).toBeVisible();
  }

  await page.goto('cases');
  const caseLink = page.locator('a[href*="/cases/"]').first();
  if (await caseLink.count()) {
    await caseLink.click();
    await expect(page.locator('h1, h2').first()).toBeVisible();
  }

  await page.goto('agents');
  await page.locator('a[href*="/agents/"]').first().click();
  await expect(page.locator('h1, h2').first()).toBeVisible();

  expect(errors, `uncaught error(s) on detail pages: ${errors.join('; ')}`).toEqual([]);
});

// The demo is interactive, not a slideshow: a write mutates the in-memory store and
// the new record shows up in the list.
test('defining an agent adds it to the list (writes mutate the store)', async ({ page }) => {
  await page.goto('agents');
  const name = 'demo-screener-' + Math.random().toString(36).slice(2, 7);
  await page.getByLabel('agent name').fill(name);
  await page.getByLabel('system prompt').fill('screen applicants for risk');
  await page.getByRole('button', { name: 'Define agent' }).click();
  await expect(page.getByRole('link', { name }).first()).toBeVisible();
});
