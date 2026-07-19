// SPDX-License-Identifier: AGPL-3.0-or-later
// Data integrity: the numbers, badges, and firing states the demo shows are COMPUTED
// from the seeded event log by the real engine — not fabricated. Each test reads the
// value the page displays and the value the backend returns, and asserts they agree,
// so a hardcoded/pretend figure can never slip back in. Comparing against the live
// API (not a literal) also keeps these green across a seed regeneration.
import { test, expect, type Page } from '@playwright/test';
import { forcePersona, gotoReady, openFlow, api, switchRole } from './helpers';

// The queue summary strip is the case projection's own counts — assert each cell
// equals what /v1/cases/summary returns.
test('the case queue summary matches the backend summary', async ({ page }) => {
  await forcePersona(page, 'operator');
  await gotoReady(page, 'cases');
  const s = await api(
    page,
    async () =>
      (await fetch('/v1/cases/summary').then((r) => r.json())) as {
        total: number;
        by_status: { needs_review: number; in_progress: number };
        unassigned: number;
        due_soon: number;
        overdue: number;
      }
  );

  const strip = page.getByLabel('queue summary');
  await expect(strip.locator('.stat', { hasText: 'Total' })).toContainText(String(s.total));
  await expect(strip.locator('.stat', { hasText: 'Needs review' })).toContainText(
    String(s.by_status.needs_review)
  );
  await expect(strip.locator('.stat', { hasText: 'In progress' })).toContainText(
    String(s.by_status.in_progress)
  );
  await expect(strip.locator('.stat', { hasText: 'Unassigned' })).toContainText(
    String(s.unassigned)
  );
  await expect(strip.locator('.stat', { hasText: 'Overdue' })).toContainText(String(s.overdue));
});

// The decisions list's count pill is the decision projection's size for the active
// filter — with no filter it must equal the total the backend reports.
test('the decisions count pill matches the total the backend reports', async ({ page }) => {
  // builder has no decisions lens, so the list lands unfiltered and the pill shows
  // the full total.
  await forcePersona(page, 'builder');
  await gotoReady(page, 'decisions');
  const total = await api(
    page,
    async () => (await fetch('/v1/decisions?limit=0').then((r) => r.json())).total as number
  );
  await expect(page.getByLabel('filter by status')).toHaveValue('');
  await expect(page.locator('.count-pill')).toContainText(String(total));
});

// The failed-decisions lens is not a cosmetic filter: its count must equal the number
// of decisions the backend actually recorded as failed.
test('the failed-decisions lens count equals the backend failed count', async ({ page }) => {
  await forcePersona(page, 'developer');
  await gotoReady(page, 'decisions');
  const failed = await api(page, async () => {
    const r = (await fetch('/v1/decisions?status=failed&limit=0').then((x) => x.json())) as {
      total: number;
    };
    return r.total;
  });
  // The developer lens already applies status=failed on landing.
  await expect(page.getByLabel('filter by status')).toHaveValue('failed');
  await expect(page.locator('.count-pill')).toContainText(String(failed));
  expect(failed).toBeGreaterThan(0); // the seed deliberately fails ~14 mid-graph
});

// The Operator deck's KPIs are derived from the same projections the API exposes —
// each headline number must reconcile with a live query.
test('the operator deck KPIs reconcile with the API', async ({ page }) => {
  await forcePersona(page, 'operator');
  await gotoReady(page, '');
  const src = await api(page, async () => {
    const j = (r: Response) => r.json();
    const decisions = (await fetch('/v1/decisions?limit=0').then(j)).total as number;
    const summary = (await fetch('/v1/cases/summary').then(j)) as {
      by_status: { needs_review: number };
    };
    const flows = ((await fetch('/v1/flows').then(j)).flows as unknown[]).length;
    const runs = (await fetch('/v1/agent-runs/summary').then(j)) as { total: number };
    return { decisions, needsReview: summary.by_status.needs_review, flows, runs: runs.total };
  });

  const kpi = (label: string) => page.locator('.kpi', { hasText: label }).locator('.kpi-num');
  await expect(kpi('Decisions')).toHaveText(String(src.decisions));
  await expect(kpi('Cases in review')).toHaveText(String(src.needsReview));
  await expect(kpi('Live flows')).toHaveText(String(src.flows));
  await expect(kpi('Agent runs')).toHaveText(String(src.runs));
});

// The manager persona-home tiles [pending_approvals, needs_review, overdue] are
// derived; check each against its source query.
test('the manager home tiles reconcile with the API', async ({ page }) => {
  await forcePersona(page, 'manager');
  await gotoReady(page, '');
  const src = await api(page, async () => {
    const j = (r: Response) => r.json();
    const summary = (await fetch('/v1/cases/summary').then(j)) as {
      by_status: { needs_review: number };
      overdue: number;
    };
    // pending_approvals = open deployment requests across every flow.
    const flows = (await fetch('/v1/flows').then(j)).flows as { flow_id: string }[];
    let pending = 0;
    for (const f of flows) {
      const fv = (await fetch(`/v1/flows/${f.flow_id}`).then(j)) as {
        deployment_requests?: { status: string }[];
      };
      pending += (fv.deployment_requests ?? []).filter((d) => d.status === 'pending').length;
    }
    return { pending, needsReview: summary.by_status.needs_review, overdue: summary.overdue };
  });

  const glance = page.getByRole('region', { name: 'At a glance' });
  await expect(glance).toContainText(String(src.pending));
  await expect(glance).toContainText(String(src.needsReview));
  await expect(glance).toContainText(String(src.overdue));
  expect(src.pending).toBeGreaterThan(0); // the seed leaves production requests pending
});

// A monitor's firing state is evaluated live over the flow's real metrics — the badge
// is not decoration. Assert the UI's firing set for a flow matches the engine's, per
// monitor, for both a firing flow and a healthy one.
async function monitorStates(page: Page, slug: string): Promise<Record<string, boolean>> {
  await openFlow(page, slug);
  await page.getByTestId('tab-monitor').click();
  const panel = page.getByTestId('monitors-panel');
  await expect(panel).toBeVisible();
  // Only monitor rows carry a .mon-state badge (webhook/schedule rows reuse .mon-list
  // but have no state), so this selects exactly the flow's monitors.
  const rows = panel.locator('.mon-list li:has(.mon-state)');
  await expect(rows.first()).toBeVisible();
  const states: [string, boolean][] = [];
  for (let i = 0; i < (await rows.count()); i++) {
    const row = rows.nth(i);
    const metric = (await row.locator('.mon-rule b').innerText()).trim();
    states.push([metric, (await row.locator('.mon-state').innerText()).trim() === 'firing']);
  }
  return Object.fromEntries(states);
}

test('firing monitors reflect the real computed engine state', async ({ page }) => {
  await forcePersona(page, 'developer');
  await gotoReady(page, '');

  // credit-decision breaches its failure-rate threshold in the seed; its other two
  // monitors are within bounds.
  const credit = await monitorStates(page, 'credit-decision');
  expect(credit['failure_rate'], 'credit-decision failure_rate should be firing').toBe(true);
  expect(credit['refer_rate']).toBe(false);

  // Cross-check the UI's firing verdict against the backend's evaluation for the same
  // flow — the badge must not diverge from the engine.
  const apiFiring = await api(page, async () => {
    const j = (r: Response) => r.json();
    const flows = (await fetch('/v1/flows').then(j)).flows as { slug: string; flow_id: string }[];
    const flow = flows.find((f) => f.slug === 'credit-decision');
    if (!flow) throw new Error('credit-decision missing from the seed');
    const mons = (await fetch(`/v1/flows/${flow.flow_id}/monitors`).then(j)).monitors as {
      metric: string;
      status: { firing: boolean };
    }[];
    return Object.fromEntries(mons.map((m) => [m.metric, m.status.firing]));
  });
  expect(credit).toEqual(apiFiring);

  // kyc-onboarding is healthy — nothing firing.
  const kyc = await monitorStates(page, 'kyc-onboarding');
  expect(
    Object.values(kyc).some((f) => f),
    'kyc-onboarding should have no firing monitor'
  ).toBe(false);
});

// The list length a page renders must equal the collection size the backend holds —
// the flows table is exactly the seeded fleet, no more, no less.
test('the flows table renders exactly the backend fleet', async ({ page }) => {
  await forcePersona(page, 'builder');
  await gotoReady(page, 'engine');
  const flows = await api(
    page,
    async () => (await fetch('/v1/flows').then((r) => r.json())).flows as { slug: string }[]
  );
  const rows = page.locator('tbody tr');
  await expect(rows).toHaveCount(flows.length);
  // And a known seed slug is present (the fleet is the seeded one, not empty/random).
  await expect(page.getByRole('cell', { name: 'credit-decision' })).toBeVisible();
});

// The four-eyes queue count the deck advertises is real: the pending-approvals callout
// equals the number of pending deployment requests, and each corresponds to a flow a
// checker can actually act on. This ties a dashboard number to a governance state.
test('the pending-approvals callout equals the real pending request count', async ({ page }) => {
  await forcePersona(page, 'operator');
  await gotoReady(page, '');
  const pending = await api(page, async () => {
    const j = (r: Response) => r.json();
    const flows = (await fetch('/v1/flows').then(j)).flows as { flow_id: string }[];
    let n = 0;
    for (const f of flows) {
      const fv = (await fetch(`/v1/flows/${f.flow_id}`).then(j)) as {
        deployment_requests?: { status: string }[];
      };
      n += (fv.deployment_requests ?? []).filter((d) => d.status === 'pending').length;
    }
    return n;
  });
  expect(pending).toBeGreaterThan(0);
  const noun = pending === 1 ? 'deploy' : 'deploys';
  await expect(page.getByText(`${pending} production ${noun} awaiting four-eyes`)).toBeVisible();
});

// A guard so the switchRole/import stays used and the suite exercises the API under a
// non-admin identity too (reads must still reconcile for a viewer).
test('read-model numbers reconcile for a viewer as well', async ({ page }) => {
  await forcePersona(page, 'operator');
  await gotoReady(page, 'cases');
  await switchRole(page, 'viewer');
  await page.reload();
  const s = await api(
    page,
    async () => (await fetch('/v1/cases/summary').then((r) => r.json())).total as number
  );
  await expect(
    page.getByLabel('queue summary').locator('.stat', { hasText: 'Total' })
  ).toContainText(String(s));
});
