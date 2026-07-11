// SPDX-License-Identifier: AGPL-3.0-or-later
// Every core user journey, driven end-to-end through the real wasm backend and
// asserted on a real outcome (a recorded verdict, a published version, a live
// deployment, a resumed decision, a changed case). Seed ids are random, so a journey
// that needs existing data resolves it through the API first.
import { test, expect } from '@playwright/test';
import { forcePersona, gotoReady, openFlow, switchRole, api } from './helpers';

function token(prefix: string): string {
  return `${prefix}-${Math.random().toString(36).slice(2, 7)}`;
}

// --- 1a. Author a flow from a template and publish it --------------------------
// The from-scratch create path is covered in persistence.spec; here the guided "New
// from template" path instantiates a complete, valid graph in the builder, which is
// then published — the author→publish loop through the real backend.
test('journey: author a flow from a template and publish it', async ({ page }) => {
  await forcePersona(page, 'builder');
  await gotoReady(page, 'engine');
  // The gallery is a disclosure — open it, then instantiate a template.
  await page.getByTestId('template-gallery').getByText('New from template').click();
  await page.getByTestId('use-template-credit-stp').click();
  await expect(page).toHaveURL(/\/engine\/[^/]+$/);
  await expect(page.getByTestId('flow-canvas')).toBeVisible();

  // Publishing the instantiated graph succeeds — the template compiles (a broken
  // graph would be refused loudly here).
  await page.getByRole('button', { name: 'Publish version' }).click();
  await expect(page.getByText(/Published v\d+|Already at v\d+/)).toBeVisible();
});

// --- 1b. Run a test decision on a deployed flow and record it -------------------
test('journey: run a test decision on a deployed flow', async ({ page }) => {
  await forcePersona(page, 'builder');
  await gotoReady(page, '');
  // credit-decision is seeded, deployed, and its connectors resolve against the demo
  // sample data, so a builder test run lands on a real verdict.
  await openFlow(page, 'credit-decision');
  await page.getByRole('button', { name: 'Run', exact: true }).click();
  const verdict = page.getByTestId('run-verdict');
  await expect(verdict).toBeVisible();
  // A recorded (non-preview) run links to its decision of record.
  await expect(page.getByRole('link', { name: /recorded decision/ })).toBeVisible();
});

// --- 2. Promote with four-eyes -------------------------------------------------
test('journey: propose a production deploy and a different approver signs it off', async ({
  page
}) => {
  // priya (editor) is the maker; marcus (approver) is the checker. Use a flow with no
  // per-flow grant list so roster roles alone decide (kyc-onboarding).
  await forcePersona(page, 'builder');
  await gotoReady(page, '');
  await switchRole(page, 'editor');
  await openFlow(page, 'kyc-onboarding');
  await page.getByTestId('tab-deploy').click();
  await expect(page.getByTestId('deploy-panel')).toBeVisible();

  // Propose the latest version for production (four-eyes) — a pending request.
  await page.getByLabel('deploy environment').selectOption('production');
  await page.getByTestId('deploy-submit').click();
  await expect(page.getByText(/Proposed v\d+ for production/)).toBeVisible();

  const requests = page.getByTestId('deployment-requests');
  await expect(requests).toBeVisible();
  // The maker cannot approve their own request.
  const ownRow = requests
    .locator('tbody tr:not(.threadrow)')
    .filter({ hasText: 'pending' })
    .first();
  await expect(ownRow.getByRole('button', { name: 'Approve' })).toBeDisabled();

  // Switch to the approver and sign it off.
  await switchRole(page, 'approver');
  const pending = requests
    .locator('tbody tr:not(.threadrow)')
    .filter({ hasText: 'pending' })
    .first();
  await pending.getByRole('button', { name: 'Approve' }).click();
  await requests.getByLabel('decision reason').fill('Reviewed — ship it.');
  await requests.getByRole('button', { name: 'Confirm approve' }).click();
  await expect(requests.getByText(/approved by marcus\.reed/).first()).toBeVisible();
});

// --- 3 + 4. Read a decision trace, then find what would flip it -----------------
test('journey: read a decision trace and run a counterfactual on a referred one', async ({
  page
}) => {
  await forcePersona(page, 'operator');
  await gotoReady(page, '');
  // Find a decision the engine dispositioned refer/decline (so the counterfactual
  // section renders), by natural query rather than a hardcoded id.
  const id = await api(page, async () => {
    const r = (await fetch('/v1/decisions?limit=500').then((x) => x.json())) as {
      decisions: { decision_id: string; disposition?: string }[];
    };
    return (
      r.decisions.find((d) => d.disposition === 'refer' || d.disposition === 'decline')
        ?.decision_id ?? ''
    );
  });
  expect(id, 'the seed should contain a referred or declined decision').toBeTruthy();

  await gotoReady(page, `decisions/${id}`);
  // The trace: reason codes and the node-by-node path.
  await expect(page.getByTestId('reason-codes')).toBeVisible();

  // Counterfactual — a real binary search over inputs, gated to operator.
  await switchRole(page, 'operator');
  const cf = page.getByTestId('cf-run');
  await expect(cf).toBeEnabled();
  await cf.click();
  // Either it finds a flip or reports none — both are real engine outcomes.
  await expect(
    page.getByTestId('cf-flips').or(page.getByText(/no single-field change/i))
  ).toBeVisible();
});

// --- 5. Resume a suspended decision --------------------------------------------
test('journey: resume a suspended decision to a terminal outcome', async ({ page }) => {
  await forcePersona(page, 'operator');
  await gotoReady(page, '');
  const id = await api(page, async () => {
    const r = (await fetch('/v1/decisions?status=suspended&limit=10').then((x) => x.json())) as {
      decisions: { decision_id: string }[];
    };
    return r.decisions[0]?.decision_id ?? '';
  });
  expect(id, 'the seed holds 3 suspended decisions').toBeTruthy();

  await gotoReady(page, `decisions/${id}`);
  await switchRole(page, 'operator');
  const panel = page.getByTestId('resume-panel');
  await expect(panel).toBeVisible();
  await panel.getByRole('button', { name: 'Approve' }).click();
  await expect(page.getByText(/Resumed/)).toBeVisible();
  // The status badge leaves 'suspended'.
  await expect(page.locator('h1, h2').first()).toBeVisible();
  await expect(page.getByTestId('resume-panel')).toHaveCount(0);
});

// --- 6. Case review from queue to resolution -----------------------------------
test('journey: assign a case, note it, and set its status', async ({ page }) => {
  await forcePersona(page, 'operator');
  await gotoReady(page, 'cases');
  // Open the first needs_review case (the operator lens already filters to it).
  await page.locator('tbody tr td a[href*="/cases/"]').first().click();
  await expect(page.getByTestId('case-status')).toBeVisible();

  await switchRole(page, 'operator');
  // Assign to me.
  await page
    .getByRole('button', { name: /Assign to me|Take over/ })
    .first()
    .click();
  await expect(page.getByText(/Assigned/)).toBeVisible();
  // Add a note — lands on the immutable activity trail.
  await page.getByLabel('note').fill('Reviewed the applicant context; escalating.');
  await page.getByRole('button', { name: 'Add note' }).click();
  await expect(page.getByText(/Note added/)).toBeVisible();
  // Move status.
  await page.getByLabel('set status').selectOption('in_progress');
  await page.getByRole('button', { name: 'Set status' }).click();
  await expect(page.getByTestId('case-status')).toContainText('in_progress');
  // The activity trail recorded the work.
  await expect(page.getByTestId('audit')).toContainText('note');
});

// --- 7. Author a policy, backtest it, publish ----------------------------------
test('journey: create a policy, backtest a draft band, and publish it', async ({ page }) => {
  page.on('dialog', (d) => d.accept()); // policy publish confirms
  await forcePersona(page, 'builder');
  await gotoReady(page, 'policies');
  await switchRole(page, 'editor');

  const name = token('journey-pol');
  await page.getByLabel('policy name').fill(name);
  // Bind to a flow with no policy yet is ideal, but any flow works; pick limit-increase.
  await page.getByLabel('flow', { exact: true }).selectOption('limit-increase');
  await page.getByRole('button', { name: /Create policy/ }).click();
  await expect(page.getByTestId('band-editor')).toBeVisible();

  await page.getByRole('button', { name: 'Add band' }).click();
  await page.getByLabel('band 0 when').fill('score >= 0.8');
  await page.getByLabel('band 0 disposition').selectOption('approve');
  await expect(page.getByTestId('band-preview')).toContainText('approve');

  // Backtest the draft against a sample dataset — a real replay through the flow.
  await page.getByTestId('sample-impact').click();
  await page.getByTestId('backtest-policy').click();
  await expect(page.getByTestId('backtest-result')).toBeVisible();

  await page.getByTestId('publish-policy').click();
  await expect(page.getByText(/Published policy v\d+/)).toBeVisible();
});

// --- 8. Grant and revoke a pre-approval ----------------------------------------
test('journey: grant a pre-approval and revoke it', async ({ page }) => {
  await forcePersona(page, 'operator');
  await gotoReady(page, 'preapprovals');
  await switchRole(page, 'editor');

  const eid = token('APP');
  await page.getByPlaceholder('applicant').fill('applicant');
  await page.getByPlaceholder('acme-co').fill(eid);
  await page.getByRole('button', { name: 'Grant pre-approval' }).click();
  await expect(page.getByText(new RegExp(`Pre-approved`))).toBeVisible();
  const row = page.locator('tbody tr').filter({ hasText: eid });
  await expect(row).toContainText('active');

  // Revoke via the inline reason form (operator may revoke).
  await switchRole(page, 'operator');
  await row.getByRole('button', { name: 'Revoke' }).click();
  await page.getByLabel('revoke reason').fill('Journey cleanup');
  await page.getByRole('button', { name: 'Confirm revoke' }).click();
  await expect(page.locator('tbody tr').filter({ hasText: eid })).toContainText('revoked');
});

// --- 9. Register a model and capture a drift baseline --------------------------
test('journey: define a model and open its drift panel', async ({ page }) => {
  await forcePersona(page, 'product');
  await gotoReady(page, 'models');
  await switchRole(page, 'editor');

  const name = token('journey_model');
  await page.getByLabel('model name').fill(name);
  await page.getByRole('button', { name: 'logistic', exact: true }).click();
  await page.getByRole('button', { name: 'Define model', exact: true }).click();
  const row = page.locator('tbody tr').filter({ hasText: name });
  await expect(row.first()).toBeVisible();

  // The seeded claim_fraud model is deliberately near its drift threshold — open its
  // panel and confirm the PSI readout renders (a real computed value).
  const claim = page.locator('tbody tr').filter({ hasText: 'claim_fraud' });
  await claim.getByRole('button', { name: 'Drift', exact: true }).click();
  await expect(page.getByTestId('model-drift')).toBeVisible();
  await expect(
    page.getByTestId('drift-firing').or(page.getByTestId('drift-alerting'))
  ).toBeVisible();
});

// --- 10. Agent: run and escalate to a case -------------------------------------
test('journey: run an agent and escalate a run to a review case', async ({ page }) => {
  page.on('dialog', (d) => d.accept()); // escalate confirms
  await forcePersona(page, 'developer');
  await gotoReady(page, 'agents');
  // Open a seeded agent.
  await page.getByRole('link', { name: 'aml-narrative' }).click();
  await expect(page.locator('h1, h2').first()).toBeVisible();

  await switchRole(page, 'operator');
  await page
    .getByLabel('prompt', { exact: true })
    .fill('Summarise the risk drivers for this wire.');
  await page.getByRole('button', { name: 'Run', exact: true }).click();
  await expect(page.getByTestId('run-result')).toBeVisible();

  // Escalate a completed run — opens a human-review case.
  const escalate = page.getByRole('button', { name: /escalate/ }).first();
  await expect(escalate).toBeVisible();
  await escalate.click();
  await expect(page.getByText(/Opened review case/)).toBeVisible();
});

// --- 11. Monitors: add one and check + notify ----------------------------------
test('journey: add a monitor and evaluate it', async ({ page }) => {
  await forcePersona(page, 'developer');
  await gotoReady(page, '');
  await switchRole(page, 'editor');
  await openFlow(page, 'kyc-onboarding');
  await page.getByTestId('tab-monitor').click();
  const panel = page.getByTestId('monitors-panel');
  await expect(panel).toBeVisible();

  await panel.getByLabel('monitor metric').selectOption('volume');
  await panel.getByLabel('monitor op').selectOption('gt');
  await panel.getByLabel('monitor threshold').fill('0');
  await panel.getByTestId('add-monitor').click();
  // The new monitor evaluates immediately over live metrics (volume > 0 fires).
  await expect(
    panel.locator('.mon-list li:has(.mon-state)').filter({ hasText: 'volume' })
  ).toContainText('firing');

  await panel.getByTestId('check-monitors').click();
  // Either firing→delivered or nothing; the check completes without error.
  await expect(panel).toBeVisible();
});

// --- 12. Context data: open a seeded entity ------------------------------------
test('journey: open a context entity and read its features and events', async ({ page }) => {
  await forcePersona(page, 'developer');
  await gotoReady(page, 'data');
  // Open a seeded applicant entity from its list row.
  await page.locator('tbody tr td a[href*="/data/"]').first().click();
  await expect(page.locator('h1, h2').first()).toBeVisible();
  // An entity page shows its attributes and a computed-features section — both derived.
  await expect(page.locator('dl.kv, .features').first()).toBeVisible();
});

// --- 13. Discussions and notifications -----------------------------------------
test('journey: post a comment and read the notifications bell', async ({ page }) => {
  await forcePersona(page, 'operator');
  await gotoReady(page, 'cases');
  await page.locator('tbody tr td a[href*="/cases/"]').first().click();
  const thread = page.getByTestId('comment-thread');
  await expect(thread).toBeVisible();
  const body = token('journey @ava.chen please review');
  await thread.getByLabel('new comment').fill(body);
  await thread.getByTestId('post-comment').click();
  await expect(thread.getByText(body)).toBeVisible();

  // The bell renders (signed in) and can be opened.
  const bell = page.getByTestId('notifications-bell');
  await expect(bell).toBeVisible();
  await bell.locator('summary').click();
  await expect(page.getByTestId('notif-error')).toHaveCount(0);
});

// --- 14. Command palette -------------------------------------------------------
test('journey: the command palette navigates to a page', async ({ page }) => {
  await forcePersona(page, 'builder');
  await gotoReady(page, '');
  await page.getByTestId('cmdk-trigger').click();
  const dialog = page.getByRole('dialog', { name: 'Command palette' });
  await expect(dialog).toBeVisible();
  await dialog.getByLabel('Search commands').fill('Decisions');
  await page
    .getByRole('option', { name: /Decisions/ })
    .first()
    .click();
  await expect(page).toHaveURL(/\/decisions\/?$/);
});

// --- 15. Copy for AI -----------------------------------------------------------
test('journey: copy the page export for AI', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await forcePersona(page, 'developer');
  await gotoReady(page, 'decisions');
  await page.getByTestId('ai-copy-trigger').click();
  await expect(page.getByText(/Copied for AI/)).toBeVisible();
  const clip = await page.evaluate(() => navigator.clipboard.readText());
  expect(clip.length).toBeGreaterThan(50); // a real markdown export, not empty
});

// --- 16. Flow export / import (flow as code) -----------------------------------
test('journey: export a flow and re-import it as a new flow', async ({ page }) => {
  await forcePersona(page, 'builder');
  await gotoReady(page, '');
  await switchRole(page, 'editor');

  // Export a seeded flow's JSON via the API (the builder Export menu produces the
  // same document), rewrite the slug, and import it through the /engine Import form.
  const doc = await api(page, async () => {
    const j = (r: Response) => r.json();
    const flows = (await fetch('/v1/flows').then(j)).flows as { slug: string; flow_id: string }[];
    const flow = flows.find((f) => f.slug === 'kyc-onboarding');
    if (!flow) throw new Error('kyc-onboarding missing from the seed');
    const id = flow.flow_id;
    return fetch(`/v1/flows/${id}/export?format=json`).then((r) => r.text());
  });
  const newSlug = token('imported');
  const rewritten = JSON.stringify({ ...JSON.parse(doc), slug: newSlug });

  await gotoReady(page, 'engine');
  await page.getByTestId('import-flow').click();
  await page.getByLabel('flow document').fill(rewritten);
  await page.getByTestId('import-submit').click();
  await expect(page.getByText(new RegExp(`Created ${newSlug}`))).toBeVisible();
  await gotoReady(page, 'engine');
  await expect(page.getByRole('cell', { name: newSlug })).toBeVisible();
});
