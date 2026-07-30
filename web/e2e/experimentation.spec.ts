// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect, type APIRequestContext } from '@playwright/test';

const KEY = 'dev-sandbox-key';
const unique = (prefix: string) => `${prefix}-${Math.random().toString(36).slice(2, 9)}`;
const headers = (key = KEY) => ({ 'X-Api-Key': key });

const constGraph = (value: string) => ({
  nodes: [
    { id: 'in', type: 'input' },
    {
      id: 'assign',
      type: 'assignment',
      config: { assignments: [{ target: 'decision', expr: `'${value}'` }] }
    },
    { id: 'out', type: 'output', config: { fields: ['decision'] } }
  ],
  edges: [
    { from: 'in', to: 'assign' },
    { from: 'assign', to: 'out' }
  ]
});

async function createVersionedFlow(
  request: APIRequestContext,
  key = KEY
): Promise<{ flowId: string; slug: string }> {
  const slug = unique('experiment-flow');
  const created = await request.post('/v1/flows', {
    headers: headers(key),
    data: { slug, name: `Experiment flow ${slug}` }
  });
  expect(created.ok()).toBeTruthy();
  const { flow_id: flowId } = (await created.json()) as { flow_id: string };
  for (const value of ['control', 'treatment']) {
    const published = await request.post(`/v1/flows/${flowId}/versions`, {
      headers: headers(key),
      data: { graph: constGraph(value) }
    });
    expect(published.ok()).toBeTruthy();
  }
  return { flowId, slug };
}

test.beforeEach(async ({ page }) => {
  const login = await page.context().request.post('/v1/login', { data: { api_key: KEY } });
  expect(login.ok()).toBeTruthy();
});

test('runs a stable experiment from setup through corrected outcome evidence', async ({
  page,
  request
}) => {
  const { flowId, slug } = await createVersionedFlow(request);
  const name = unique('Offer conversion');

  await page.goto('/experiments');
  await page.getByRole('button', { name: 'New experiment' }).click();
  await page.getByLabel('Name', { exact: true }).fill(name);
  await page.getByLabel('Hypothesis', { exact: true }).fill('The treatment increases conversion.');
  await page.getByLabel('Flow', { exact: true }).selectOption(flowId);
  await page.getByLabel('Champion version').selectOption('1');
  await page.getByLabel('Challenger version').selectOption('2');
  await page.getByLabel('Minimum observations / arm').fill('2');
  await page.getByRole('button', { name: 'Create draft' }).click();

  await expect(page.getByRole('heading', { name })).toBeVisible();
  await expect(page.getByText('No winner can be called')).toBeVisible();
  await page.getByRole('button', { name: 'Start' }).click();
  await expect(page.getByText('running', { exact: true })).toBeVisible();

  const decide = async (idempotencyKey: string) => {
    const response = await request.post(`/v1/flows/${slug}/sandbox/decide`, {
      headers: { ...headers(), 'Idempotency-Key': idempotencyKey },
      data: { data: { customer_id: 'stable-customer-42' } }
    });
    expect(response.ok()).toBeTruthy();
    return (await response.json()) as {
      decision_id: string;
      experiment_id: string;
      experiment_cohort: number;
      experiment_arm: string;
    };
  };
  const first = await decide(unique('decision'));
  const second = await decide(unique('decision'));
  expect(first.decision_id).not.toBe(second.decision_id);
  expect(first.experiment_id).toBeTruthy();
  expect(second.experiment_id).toBe(first.experiment_id);
  expect(second.experiment_cohort).toBe(first.experiment_cohort);
  expect(second.experiment_arm).toBe(first.experiment_arm);

  await page.reload();
  const exposureRows = page.locator('section').filter({
    has: page.getByRole('heading', { name: 'Reached treatments' })
  });
  await expect(exposureRows.getByRole('link', { name: first.decision_id })).toBeVisible();
  await expect(exposureRows.getByRole('link', { name: second.decision_id })).toBeVisible();

  await page.getByLabel('Reached decision').selectOption(first.decision_id);
  await page.getByLabel('Source system').fill('orders-warehouse');
  await page.getByLabel('Source record ID').fill(unique('order'));
  await page.getByRole('button', { name: 'Record outcome' }).click();
  await expect(page.getByText('1 / 1')).toBeVisible();

  await page.getByRole('button', { name: 'Correct' }).click();
  await page.getByLabel('Corrected value').fill('0');
  await page.getByLabel('Reason', { exact: true }).fill('Settlement was reversed.');
  await page.getByRole('button', { name: 'Append correction' }).click();
  await expect(page.getByText('2 / 2')).toBeVisible();
  await expect(page.getByText('No winner can be called')).toBeVisible();
  await expect(page.getByText(/^Winner:/)).toHaveCount(0);
});

test('executes and downloads a durable version-pinned population backtest', async ({
  page,
  request
}) => {
  const { slug } = await createVersionedFlow(request);
  await page.goto('/population');
  await page.getByRole('button', { name: 'New population job' }).click();
  await page.getByLabel('Flow', { exact: true }).selectOption(slug);
  await page
    .getByLabel('population dataset')
    .fill(
      JSON.stringify([
        { customer_id: 'population-1' },
        { customer_id: 'population-2' },
        { customer_id: 'population-3' }
      ])
    );
  await page.getByRole('button', { name: 'Create durable job' }).click();

  await expect(page.getByRole('heading', { name: slug })).toBeVisible();
  await expect(page.getByText('completed', { exact: true })).toBeVisible({ timeout: 15_000 });
  const rows = page.locator('tbody tr');
  await expect(rows).toHaveCount(3);
  await expect(rows.filter({ hasText: 'succeeded' })).toHaveCount(3);
  await expect(rows.first()).toContainText('v2');

  const downloadPromise = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Download NDJSON results' }).click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toMatch(/^population-.+\.ndjson$/);
});

test('hands a production experiment launch from maker to independent approver', async ({
  page,
  request
}) => {
  const suffix = Math.random().toString(36).slice(2, 9);
  const actor = `experiment-maker-${suffix}`;
  const keyResponse = await request.post('/v1/api-keys', {
    headers: headers(),
    data: { name: actor, actor, role: 'editor', scope: '*' }
  });
  expect(keyResponse.ok()).toBeTruthy();
  const { secret: makerKey } = (await keyResponse.json()) as { secret: string };
  const { flowId } = await createVersionedFlow(request, makerKey);

  const deploymentRequest = await request.post(`/v1/flows/${flowId}/deployment-requests`, {
    headers: headers(makerKey),
    data: { environment: 'production', version: 1 }
  });
  expect(deploymentRequest.ok()).toBeTruthy();
  const { request_id: deploymentRequestId } = (await deploymentRequest.json()) as {
    request_id: string;
  };
  const approved = await request.post(
    `/v1/flows/${flowId}/deployment-requests/${deploymentRequestId}/approve`,
    { headers: headers(), data: { reason: 'Initial production champion reviewed.' } }
  );
  expect(approved.ok()).toBeTruthy();

  const experimentCreated = await request.post('/v1/experiments', {
    headers: headers(makerKey),
    data: {
      name: `Production experiment ${suffix}`,
      hypothesis: 'The treatment improves approval quality.',
      owner: actor,
      flow_id: flowId,
      environment: 'production',
      subject_key_expression: 'customer_id',
      salt: `production-${suffix}`,
      arms: [
        {
          key: 'champion',
          name: 'Champion',
          kind: 'champion',
          version: 1,
          allocation_bps: 5000
        },
        {
          key: 'challenger',
          name: 'Challenger',
          kind: 'challenger',
          version: 2,
          allocation_bps: 5000
        }
      ],
      primary_metric: {
        key: 'converted',
        name: 'Conversion',
        kind: 'binary',
        direction: 'increase'
      },
      minimum_sample_per_arm: 10,
      minimum_effect: 0.01,
      confidence: 0.95,
      observation_window_days: 30
    }
  });
  expect(experimentCreated.ok()).toBeTruthy();
  const { experiment_id: experimentId } = (await experimentCreated.json()) as {
    experiment_id: string;
  };

  await page.context().clearCookies();
  expect(
    (
      await page.context().request.post('/v1/login', {
        data: { api_key: makerKey }
      })
    ).ok()
  ).toBeTruthy();
  await page.goto(`/experiments/${experimentId}`);
  await page.getByRole('button', { name: 'Start' }).click();
  await expect(page.getByText('pending launch', { exact: true })).toBeVisible();
  await expect(page.getByText('Waiting for an independent approver.')).toBeVisible();

  await page.context().clearCookies();
  expect(
    (
      await page.context().request.post('/v1/login', {
        data: { api_key: KEY }
      })
    ).ok()
  ).toBeTruthy();
  await page.goto(`/experiments/${experimentId}`);
  await page.getByLabel('action reason').fill('Independent risk review passed.');
  await page.getByRole('button', { name: 'Approve launch' }).click();
  await expect(page.getByText('running', { exact: true })).toBeVisible();
});
