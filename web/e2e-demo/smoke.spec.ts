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

// A builder preview run produces a verdict but records nothing (the Test tab is the
// default tab, so the controls are immediately available after opening a flow).
test('a builder preview run shows a verdict but records no decision', async ({ page }) => {
  await page.goto('engine');
  await page.locator('a[href*="/engine/"]').first().click();
  await page.getByLabel("preview (don't record)").check();
  await page.getByRole('button', { name: 'Run', exact: true }).click();
  await expect(page.getByTestId('run-verdict')).toBeVisible();
  await expect(page.getByText('preview · not recorded')).toBeVisible();
  await expect(page.getByText('View the recorded decision')).toHaveCount(0);
});

// The demo identity switcher changes the signed-in role, and role-gated nav reacts
// live: admin-only surfaces (Model risk, Audit) vanish for a non-admin viewer.
test('switching demo role updates the role-gated navigation', async ({ page }) => {
  // The manager persona's nav includes the admin-only items, so gating is observable.
  await page.addInitScript(() => localStorage.setItem('intraktible-persona', 'manager'));
  await page.goto('');
  // Scope to the primary nav (the role-gated surface); "Model risk" also appears as a
  // persona-home action chip.
  const navModelRisk = page
    .getByRole('navigation', { name: 'Primary' })
    .getByRole('link', { name: 'Model risk' });
  await expect(navModelRisk).toBeVisible(); // default identity is the admin (Ava)

  await page
    .getByLabel('Demo user (switch acting identity)')
    .selectOption({ label: 'Lena Hoff · viewer' });
  await expect(navModelRisk).toHaveCount(0); // viewer loses the admin-only surface
});

// Gating is server-side, not just nav-hiding: a non-admin who types the URL directly
// hits the restricted state rather than seeing admin content (matches the real RBAC).
test('a non-admin reaching an admin-only page directly sees the restricted state', async ({
  page
}) => {
  await page.goto('');
  await page
    .getByLabel('Demo user (switch acting identity)')
    .selectOption({ label: 'Lena Hoff · viewer' });
  await page.goto('mrm');
  await expect(page.getByText('Restricted to the admin role')).toBeVisible();
});

// The switched identity persists across a reload (it's saved like every other write),
// so a mid-flow refresh doesn't silently revert you to the default admin.
test('the switched demo user survives a reload', async ({ page }) => {
  await page.goto('');
  const switcher = page.getByLabel('Demo user (switch acting identity)');
  await switcher.selectOption({ label: 'Diego Santos · operator' });
  await page.reload();
  await expect(page.getByLabel('Demo user (switch acting identity)')).toHaveValue(
    'diego.santos@intraktible.dev'
  );
});

// The demo is interactive, not a slideshow: a write mutates the in-memory store and
// the new record shows up in the list.
test('defining an agent adds it to the list (writes mutate the store)', async ({ page }) => {
  await page.goto('agents');
  // The "Define agent" form is a disclosure (content-first); open it.
  await page.getByText('+ Define agent').click();
  const name = 'demo-screener-' + Math.random().toString(36).slice(2, 7);
  await page.getByLabel('agent name').fill(name);
  await page.getByLabel('system prompt').fill('screen applicants for risk');
  await page.getByRole('button', { name: 'Define agent' }).click();
  await expect(page.getByRole('link', { name }).first()).toBeVisible();
});

// State is persisted to localStorage, so a created flow survives a full reload —
// the demo accumulates progress instead of resetting every page view.
test('a created flow persists across a reload', async ({ page }) => {
  await page.goto('engine');
  const slug = 'persist-' + Math.random().toString(36).slice(2, 7);
  await page.getByLabel('slug', { exact: true }).fill(slug);
  await page.getByLabel('name', { exact: true }).fill('Persist Test');
  await page.getByRole('button', { name: 'Create flow' }).click();
  // Create navigates into the new flow's builder; return to the list to see the row.
  await page.goto('engine');
  await expect(page.getByRole('cell', { name: slug })).toBeVisible();

  await page.reload();
  await expect(page.getByRole('cell', { name: slug })).toBeVisible();
});

// Reset clears local state and restores the seed — a flow created this session is
// gone after a reset.
test('reset restores the seed', async ({ page }) => {
  page.on('dialog', (d) => d.accept());
  await page.goto('engine');
  const slug = 'reset-' + Math.random().toString(36).slice(2, 7);
  await page.getByLabel('slug', { exact: true }).fill(slug);
  await page.getByLabel('name', { exact: true }).fill('Reset Test');
  await page.getByRole('button', { name: 'Create flow' }).click();
  // Create navigates into the new flow's builder; return to the list to see the row.
  await page.goto('engine');
  await expect(page.getByRole('cell', { name: slug })).toBeVisible();

  await page.getByRole('button', { name: 'Reset' }).click();
  // Reset reloads the page with the pristine seed; the created flow is gone.
  await expect(page.getByRole('cell', { name: slug })).toHaveCount(0);
  await expect(page.getByRole('cell', { name: 'credit-decision' })).toBeVisible();
});

// Maker-checker is real: a request created by one user can't be self-approved, and
// an approver who is a different user can approve it. Driven through the in-browser
// backend (window.fetch) so the four-eyes rule is exercised directly.
test('maker-checker four-eyes is enforced across users', async ({ page }) => {
  await page.goto('');
  // The mock backend (window.fetch + window.__demo) is installed during the layout
  // load; wait for it before driving requests so they don't race onto the preview.
  await page.waitForFunction(() => '__demo' in window);
  const result = await page.evaluate(async () => {
    const j = (r: Response) => r.json();
    // Act as the editor (maker) and request a production deployment of credit-decision.
    await fetch('/v1/login', { method: 'POST', body: JSON.stringify({ api_key: 'x' }) });
    const flows = (await fetch('/v1/flows').then(j)).flows;
    const flow = flows.find((f: { slug: string }) => f.slug === 'credit-decision');
    const w = window as unknown as {
      __demo: { setUser(a: string): void; users: { actor: string; role: string }[] };
    };
    // Maker is the approver (so we test four-eyes, not the role gate); checker is the
    // admin (a different user who also outranks the approve gate).
    const maker = w.__demo.users.find((u) => u.role === 'approver')?.actor ?? '';
    const checker = w.__demo.users.find((u) => u.role === 'admin')?.actor ?? '';
    w.__demo.setUser(maker);
    const req = await fetch(`/v1/flows/${flow.flow_id}/deployment-requests`, {
      method: 'POST',
      body: JSON.stringify({ environment: 'production', version: flow.latest })
    }).then(j);
    // The maker cannot approve their own request (four-eyes).
    const selfApprove = await fetch(
      `/v1/flows/${flow.flow_id}/deployment-requests/${req.request_id}/approve`,
      { method: 'POST', body: '{}' }
    );
    // A different, sufficiently-privileged user can.
    w.__demo.setUser(checker);
    const checkerApprove = await fetch(
      `/v1/flows/${flow.flow_id}/deployment-requests/${req.request_id}/approve`,
      { method: 'POST', body: '{}' }
    );
    // An editor (below approver) is refused outright by the role gate.
    const editor = w.__demo.users.find((u) => u.role === 'editor')?.actor ?? '';
    w.__demo.setUser(editor);
    const req2 = await fetch(`/v1/flows/${flow.flow_id}/deployment-requests`, {
      method: 'POST',
      body: JSON.stringify({ environment: 'production', version: flow.latest })
    }).then(j);
    const editorApprove = await fetch(
      `/v1/flows/${flow.flow_id}/deployment-requests/${req2.request_id}/approve`,
      { method: 'POST', body: '{}' }
    );
    return {
      self: selfApprove.status,
      checker: checkerApprove.status,
      editor: editorApprove.status
    };
  });
  expect(result.self).toBe(400); // self-approval rejected (four-eyes)
  expect(result.checker).toBe(200); // a different, privileged user succeeds
  expect(result.editor).toBe(403); // below the approver role is refused
});
