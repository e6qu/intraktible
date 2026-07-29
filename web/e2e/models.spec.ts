// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('defines a predictive model from the registry page', async ({ page }) => {
  await page.goto('/models');
  await expect(page.getByRole('heading', { name: 'Models' })).toBeVisible();

  const name = 'risk-' + Math.random().toString(36).slice(2, 8);
  await page.getByLabel('model name').fill(name);
  // The logistic starter is preloaded; define it.
  await page.getByRole('button', { name: 'Define model' }).click();

  const row = page.locator('tbody tr').filter({ hasText: name });
  await expect(row).toBeVisible();
  await expect(row.getByText('logistic')).toBeVisible();

  // The drift readout opens; with no predictions yet it explains how to get data.
  await row.getByRole('button', { name: 'Drift' }).click();
  const driftRow = page.getByTestId('model-drift');
  await expect(driftRow).toBeVisible();
  await expect(driftRow).toContainText('No predictions recorded yet');

  // Ground truth can arrive before this deployment has produced an in-platform
  // prediction. Recording it through the real UI must preserve it and immediately
  // turn the previously read-only performance panel into measured evidence.
  await page.getByLabel('actual predicted probability').fill('0.9');
  await page.getByLabel('actual outcome').selectOption('1');
  await page.getByLabel('actual decision id').fill('decision-lineage-1');
  await page.getByRole('button', { name: 'Record actual' }).click();
  await expect(page.getByTestId('model-performance')).toContainText(
    'Live performance (from 1 recorded actual)'
  );

  // The author can request review but cannot approve their own model.
  await row.getByRole('button', { name: 'Governance' }).click();
  const governance = page.getByTestId('model-governance');
  await governance.getByRole('button', { name: 'Request approval' }).click();
  const selfApprove = governance.getByRole('button', { name: 'Approve' });
  await expect(selfApprove).toBeDisabled();
  await expect(selfApprove).toHaveAttribute('title', /four-eyes/i);
});

test('a failed model drift summary is visibly unavailable', async ({ page, request }) => {
  const name = 'drift-read-' + Math.random().toString(36).slice(2, 8);
  const defined = await request.post('/v1/models', {
    headers: { 'X-Api-Key': KEY },
    data: {
      name,
      spec: { kind: 'logistic', intercept: -3, coefficients: { fico: 0.005 } }
    }
  });
  expect(defined.ok()).toBeTruthy();
  await page.route(`**/v1/models/${name}/drift`, (route) =>
    route.fulfill({
      status: 500,
      contentType: 'application/json',
      body: '{"error":"drift projection offline"}'
    })
  );

  await page.goto('/models');
  const row = page.locator('tbody tr').filter({ hasText: name }).first();
  const unavailable = row.getByText('unavailable');
  await expect(unavailable).toBeVisible();
  await expect(unavailable).toHaveAttribute('title', 'drift projection offline');
});

test('a different actor approves a model with recorded reasoning', async ({ page, request }) => {
  const suffix = Math.random().toString(36).slice(2, 8);
  const keyResponse = await request.post('/v1/api-keys', {
    headers: { 'X-Api-Key': KEY },
    data: { name: `model-maker-${suffix}`, actor: `maker-${suffix}`, role: 'editor', scope: '*' }
  });
  expect(keyResponse.ok()).toBeTruthy();
  const { secret } = await keyResponse.json();
  const makerHeaders = { 'X-Api-Key': secret };
  const name = `approved-${suffix}`;
  const defined = await request.post('/v1/models', {
    headers: makerHeaders,
    data: {
      name,
      spec: { kind: 'logistic', intercept: -3, coefficients: { fico: 0.005 } }
    }
  });
  expect(defined.ok()).toBeTruthy();
  const requested = await request.post(`/v1/models/${name}/approval-request`, {
    headers: makerHeaders
  });
  expect(requested.ok()).toBeTruthy();

  // The manager cockpit includes model (not only flow) approvals and exposes both
  // governance surfaces in its navigation.
  await page.goto('/');
  await page.evaluate(() => localStorage.setItem('intraktible-persona', 'manager'));
  await page.reload();
  const primary = page.getByRole('navigation', { name: 'Primary' });
  await expect(primary.getByText('Flows')).toBeVisible();
  await expect(primary.getByText('Models')).toBeVisible();
  const pending = page.getByRole('region', { name: 'Pending approvals' });
  await expect(pending).toContainText(name);
  await expect(pending).toContainText('model');

  // The shared approver notification lands directly in this model's Governance
  // panel, so the checker does not have to rediscover the pending request.
  const bell = page.getByTestId('notifications-bell');
  await bell.locator('summary').click();
  const notification = bell.locator('button.item').filter({
    hasText: `Approval requested: model ${name} v1`
  });
  await expect(notification).toBeVisible();
  await notification.click();
  await expect(page).toHaveURL(
    (url) =>
      url.pathname === '/models' &&
      url.searchParams.get('governance') === name &&
      url.hash === '#model-governance'
  );
  const governance = page.getByTestId('model-governance');
  await expect(governance).toBeVisible();
  const approve = governance.getByRole('button', { name: 'Approve' });
  await expect(approve).toBeDisabled();
  await expect(approve).toHaveAttribute('title', /independent validation/i);
  await governance.getByPlaceholder('dataset (e.g. backtest_Q1)').fill('holdout_2026Q2');
  await governance.getByPlaceholder('metrics (auc=0.81, ks=0.42)').fill('auc=0.84, ks=0.45');
  await governance
    .getByPlaceholder('independent review notes')
    .fill('Independent holdout review met the documented acceptance thresholds.');
  const validationRequest = page.waitForRequest(
    (req) => req.url().endsWith(`/v1/models/${name}/validation`) && req.method() === 'POST'
  );
  await governance.getByRole('button', { name: 'Record validation' }).click();
  expect((await validationRequest).postDataJSON()).toEqual({
    dataset: 'holdout_2026Q2',
    metrics: { auc: 0.84, ks: 0.45 },
    notes: 'Independent holdout review met the documented acceptance thresholds.',
    passed: true
  });
  await expect(governance.getByText('Current independent validation:')).toBeVisible();
  await expect(approve).toBeEnabled();
  await approve.click();
  await page.getByLabel('model decision reason').fill('Independent validation passed.');
  const approvalRequest = page.waitForRequest(
    (req) => req.url().endsWith(`/v1/models/${name}/approve`) && req.method() === 'POST'
  );
  await governance.getByRole('button', { name: 'Confirm approval' }).click();
  expect((await approvalRequest).postDataJSON()).toMatchObject({
    reason: 'Independent validation passed.'
  });
  await expect(governance.getByText('approved v1')).toBeVisible();

  // Another approver must not keep seeing already-decided work as actionable.
  await bell.locator('summary').click();
  await expect(
    bell.locator('button.item').filter({ hasText: `Approval requested: model ${name} v1` })
  ).toHaveCount(0);
});

test('trains a logistic model from a dataset and shows the report', async ({ page }) => {
  await page.goto('/models');
  await expect(page.getByRole('heading', { name: 'Models' })).toBeVisible();

  await page.getByText('Train a logistic model from a dataset').click();
  const name = 'trained-' + Math.random().toString(36).slice(2, 8);
  await page.getByLabel('model to train').fill(name);

  // A small separable dataset: label follows `signal`; `noise` is irrelevant.
  const rows: { features: Record<string, number>; label: number }[] = [];
  for (let i = 0; i < 40; i++) {
    const signal = (i % 20) / 2; // 0..9.5
    rows.push({ features: { signal, noise: (i * 3) % 7 }, label: signal >= 5 ? 1 : 0 });
  }
  await page.getByLabel('training dataset').fill(JSON.stringify(rows));
  await page.getByRole('button', { name: 'Train model' }).click();

  // The training report renders with metrics and per-feature importance.
  const report = page.getByTestId('train-report');
  await expect(report).toBeVisible();
  await expect(report).toContainText('CV AUC');
  await expect(report).toContainText('signal');

  // The trained model now appears in the registry table.
  await expect(page.locator('tbody tr').filter({ hasText: name })).toBeVisible();
});

test('a predict node panel edits model + output without raw JSON', async ({ page, request }) => {
  const slug = 'pf-' + Math.random().toString(36).slice(2, 8);
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Predict flow' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  await page.getByTestId('toggle-panel').click(); // the tools panel starts closed
  await expect(page.getByLabel('new node type')).toBeVisible();
  await page.getByLabel('new node type').selectOption('predict');
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await page.getByLabel('predict model').fill('risk');
  await page.getByLabel('predict output').fill('risk');

  await expect(page.getByLabel('node config')).toHaveValue('{"model":"risk","output":"risk"}');
});
