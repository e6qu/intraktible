// SPDX-License-Identifier: AGPL-3.0-or-later
// Personas × role gating, against the real wasm backend. A persona reshapes the
// same app (landing, nav, lens); a role gates what you can actually do. Both are
// exercised end-to-end: the persona from localStorage, the role from a real
// /v1/login as a demo-roster user.
import { test, expect, type Page } from '@playwright/test';
import { forcePersona, switchRole, trackPageErrors } from './helpers';

// The primary nav, scoped so we never match a same-named home-action chip.
function nav(page: Page) {
  return page.getByRole('navigation', { name: 'Primary' });
}
function navLink(page: Page, label: string) {
  return nav(page).getByRole('link', { name: label, exact: true });
}

// Each persona lands on its own home composition. Assert the distinguishing signal
// documented for each deck (a testid, or a heading for the two decks without one).
const HOMES: { persona: string; assert: (page: Page) => Promise<void> }[] = [
  {
    persona: 'builder',
    assert: async (p) => expect(p.getByRole('heading', { name: 'decision tape' })).toBeVisible()
  },
  {
    persona: 'developer',
    assert: async (p) => expect(p.getByTestId('persona-home')).toBeVisible()
  },
  {
    persona: 'operator',
    assert: async (p) => expect(p.getByRole('heading', { name: 'Operations' })).toBeVisible()
  },
  { persona: 'manager', assert: async (p) => expect(p.getByTestId('persona-home')).toBeVisible() },
  { persona: 'product', assert: async (p) => expect(p.getByTestId('persona-home')).toBeVisible() },
  { persona: 'showcase', assert: async (p) => expect(p.getByTestId('exec-trend')).toBeVisible() },
  {
    persona: 'evaluator',
    assert: async (p) => expect(p.getByTestId('evaluator-tour')).toBeVisible()
  }
];

for (const { persona, assert } of HOMES) {
  test(`persona ${persona} lands on its own home with no page error`, async ({ page }) => {
    const errors = trackPageErrors(page);
    await forcePersona(page, persona);
    await page.goto('');
    await assert(page);
    expect(errors, `uncaught error(s) as ${persona}: ${errors.join('; ')}`).toEqual([]);
  });
}

// The nav is the same catalog re-prioritised per persona. Spot-check that each
// persona surfaces its signature items and drops others (the whole point of the
// adaptation), as an admin so admin-only items aren't the thing being hidden.
test('each persona reprioritises the same nav catalog', async ({ page }) => {
  const cases: { persona: string; shows: string[]; hides: string[] }[] = [
    {
      persona: 'builder',
      shows: ['Flows', 'Policies', 'Models'],
      hides: ['Cases', 'Pre-approvals']
    },
    {
      persona: 'operator',
      shows: ['Cases', 'Pre-approvals', 'Decisions'],
      hides: ['Flows', 'Models']
    },
    {
      persona: 'developer',
      shows: ['Flows', 'Agents', 'Observability'],
      hides: ['Cases', 'Policies']
    },
    {
      persona: 'manager',
      shows: ['Pre-approvals', 'Cases', 'Observability'],
      hides: ['Flows', 'Agents']
    }
  ];
  for (const { persona, shows, hides } of cases) {
    await forcePersona(page, persona);
    await page.goto('');
    await expect(nav(page)).toBeVisible();
    for (const label of shows) {
      await expect(navLink(page, label), `${persona} should show ${label}`).toBeVisible();
    }
    for (const label of hides) {
      await expect(navLink(page, label), `${persona} should hide ${label}`).toHaveCount(0);
    }
  }
});

// The developer nav relabels Decisions → Traces (a persona term override), which is
// the clearest evidence the same catalog is being relabelled, not replaced.
test('the developer persona relabels Decisions as Traces', async ({ page }) => {
  await forcePersona(page, 'developer');
  await page.goto('');
  await expect(navLink(page, 'Traces')).toBeVisible();
  await expect(navLink(page, 'Decisions')).toHaveCount(0);
});

// A persona applies a default lens (an initial filter) on a shared list page only
// when the URL carries no filter of its own.
test('operator lands on the needs_review case lens', async ({ page }) => {
  await forcePersona(page, 'operator');
  await page.goto('cases');
  await expect(page.getByLabel('status filter')).toHaveValue('needs_review');
});

test('developer lands on the failed-decisions lens', async ({ page }) => {
  await forcePersona(page, 'developer');
  await page.goto('decisions');
  await expect(page.getByLabel('filter by status')).toHaveValue('failed');
});

test('product lands on the challenger-variant lens', async ({ page }) => {
  await forcePersona(page, 'product');
  await page.goto('decisions');
  await expect(page.getByLabel('filter by variant')).toHaveValue('challenger');
});

// --- Role gating ---------------------------------------------------------------

// admin-only pages (Model risk, Audit, API keys) are dropped from the nav for any
// non-admin role. The demo boots as admin (Ava), so switching to a lower role must
// make them vanish live, without navigating.
test('admin-only nav items disappear the moment the role drops below admin', async ({ page }) => {
  // manager is one of the personas whose nav includes admin-only items.
  await forcePersona(page, 'manager');
  await page.goto('');
  await expect(navLink(page, 'Model risk')).toBeVisible();
  await expect(navLink(page, 'Audit')).toBeVisible();

  await switchRole(page, 'viewer');
  await expect(navLink(page, 'Model risk')).toHaveCount(0);
  await expect(navLink(page, 'Audit')).toHaveCount(0);
});

// Defence in depth: the admin pages gate server-side too, so a non-admin who types
// the URL sees a restricted state, not the data.
test('a non-admin who navigates directly to an admin page sees the restricted state', async ({
  page
}) => {
  await forcePersona(page, 'operator');
  await page.goto('');
  await switchRole(page, 'viewer');

  await page.goto('mrm');
  await expect(page.getByText('Restricted to the admin role')).toBeVisible();

  await page.goto('keys');
  await expect(page.getByText('Restricted to the admin role')).toBeVisible();

  await page.goto('audit');
  await expect(page.getByTestId('audit-forbidden')).toBeVisible();
});

// Write actions are not hidden from a viewer — they render disabled with a title
// explaining the missing role, so the page stays a coherent read-only view.
test('a viewer sees write actions disabled with an explanatory title', async ({ page }) => {
  await forcePersona(page, 'operator');
  await page.goto('');
  await switchRole(page, 'viewer');

  await page.goto('cases');
  const openCase = page.getByRole('button', { name: 'Open case' });
  await expect(openCase).toBeDisabled();
  await expect(openCase).toHaveAttribute('title', /operator role/i);
});

// An operator can work cases but cannot author flows: the capability ladder is real,
// not cosmetic. (editor authors; operator acts on queues.)
test('the capability ladder: operator acts on cases, editor authors flows', async ({ page }) => {
  await forcePersona(page, 'builder');
  await page.goto('');

  // As an operator: the case sweep is enabled, but creating a flow is refused for a
  // lack of the editor role (the button carries that explanation).
  await switchRole(page, 'operator');
  await page.goto('cases');
  await expect(page.getByRole('button', { name: 'Run SLA sweep' })).toBeEnabled();
  await page.goto('engine');
  await expect(page.getByRole('button', { name: 'Create flow' })).toHaveAttribute(
    'title',
    /editor role/i
  );

  // As an editor: the role no longer blocks it — with a slug filled it enables.
  await switchRole(page, 'editor');
  await page.goto('engine');
  await page.getByLabel('slug', { exact: true }).fill('cap-ladder-check');
  await expect(page.getByRole('button', { name: 'Create flow' })).toBeEnabled();
});

// The identity survives a reload (re-logged-in from sessionStorage), so gating is
// stable across a refresh, not just until the next navigation.
test('the switched role survives a reload', async ({ page }) => {
  await forcePersona(page, 'operator');
  await page.goto('');
  await switchRole(page, 'viewer');
  await page.reload();
  await expect(page.getByLabel('Demo user (switch acting identity)')).toHaveValue('lena.hoff');
  await page.goto('cases');
  await expect(page.getByRole('button', { name: 'Open case' })).toBeDisabled();
});
