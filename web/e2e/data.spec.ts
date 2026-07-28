// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';
const uniq = () => Math.random().toString(36).slice(2, 9);

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('defines a connector and a feature from the UI', async ({ page }) => {
  const conn = 'conn-' + uniq();
  const feat = 'feat_' + uniq();

  await page.goto('/data');
  await expect(page.getByRole('heading', { name: /Context data/i })).toBeVisible();

  // A catalog template scaffolds the define form: clicking the (real, normalizing)
  // Credit bureau template sets type=credit_bureau and fills its config skeleton.
  const catalog = page.getByTestId('connector-catalog');
  await catalog.getByRole('button', { name: 'Credit bureau' }).click();
  await expect(page.getByLabel('connector type')).toHaveValue('credit_bureau');
  await expect(page.getByLabel('connector config')).toHaveValue(/experian/);

  // Define a connector.
  await page.getByLabel('connector name').fill(conn);
  await page.getByLabel('connector type').selectOption('mock_bureau');
  await page.getByRole('button', { name: 'Define connector' }).click();
  const connectorRow = page.locator('tbody tr').filter({ hasText: conn });
  await expect(connectorRow).toBeVisible();

  // Validate the configured source through the same resilient invocation path a
  // Connect node uses, then require its durable fetch evidence to appear.
  await connectorRow.getByRole('button', { name: 'Inspect / test' }).click();
  await page.getByLabel('connector test parameters').fill('{"subject":"applicant/e2e"}');
  await page.getByRole('button', { name: 'Test connector' }).click();
  await expect(page.getByTestId('connector-response')).toContainText('risk_score');
  await expect(page.getByTestId('connector-fetch-count')).toHaveText('(1)');

  // A connector with a credential-bearing config is masked in the list (the DSN
  // never reaches the client).
  const secretConn = 'sql-' + uniq();
  await page.getByLabel('connector name').fill(secretConn);
  await page.getByLabel('connector type').selectOption('sql');
  await page
    .getByLabel('connector config')
    .fill('{"driver":"sqlite","dsn":"user:supersecret@/db","query":"SELECT 1"}');
  await page.getByRole('button', { name: 'Define connector' }).click();
  const secretRow = page.locator('tbody tr').filter({ hasText: secretConn });
  await expect(secretRow).toContainText('[redacted]');
  await expect(secretRow).not.toContainText('supersecret');

  // Define a feature.
  await page.getByLabel('feature name').fill(feat);
  await page.getByLabel('feature entity type').fill('customer');
  await page.getByLabel('feature event name').fill('txn');
  await page.getByLabel('feature aggregation').selectOption('count');
  await page.getByLabel('feature window hours').fill('24');
  await page.getByRole('button', { name: 'Define feature' }).click();
  await expect(page.locator('tbody').filter({ hasText: feat })).toBeVisible();
});

test('creates an entity and records an event that updates its feature from the UI', async ({
  page,
  request
}) => {
  const id = 'ui-' + uniq();
  const feature = 'ui_events_' + uniq();
  await request.post('/v1/context/features', {
    headers: { 'X-Api-Key': KEY },
    data: {
      name: feature,
      entity_type: 'customer',
      event_name: 'transaction',
      aggregation: 'count',
      window_hours: 24
    }
  });

  await page.goto('/data');
  await page.getByLabel('entity type', { exact: true }).fill('customer');
  await page.getByLabel('entity id').fill(id);
  await page.getByLabel('entity attributes').fill('{"tier":"gold"}');
  await page.getByRole('button', { name: 'Create or update entity' }).click();

  const entityRow = page.locator('tbody tr').filter({ hasText: id });
  await expect(entityRow).toContainText('customer');
  await entityRow.getByRole('link', { name: id }).click();
  await expect(page.getByText('gold')).toBeVisible();

  await page.getByLabel('event name').fill('transaction');
  await page.getByLabel('event data').fill('{"amount":125}');
  await page.getByRole('button', { name: 'Record event' }).click();

  await expect(page.locator('.timeline')).toContainText('transaction');
  await expect(page.locator('.timeline')).toContainText('"amount":125');
  await expect(page.locator('.feat').filter({ hasText: feature })).toContainText('1');
});

test('lawful-basis expiry is authored and never presented as active after it lapses', async ({
  page,
  request
}) => {
  const id = 'basis-' + uniq();
  const subject = `customer/${id}`;
  await request.post('/v1/context/entities', {
    headers: { 'X-Api-Key': KEY },
    data: { entity_type: 'customer', entity_id: id, attributes: { tier: 'silver' } }
  });

  await expect(async () => {
    const response = await request.get(`/v1/context/entities/customer/${id}`, {
      headers: { 'X-Api-Key': KEY }
    });
    expect(response.ok()).toBe(true);
  }).toPass();

  await page.goto(`/data/customer/${id}`);
  await page.getByPlaceholder('purpose (e.g. credit_underwriting)').fill('marketing');
  await page.getByLabel('lawful basis').selectOption('consent');
  const expires = new Date(Date.now() + 86_400_000);
  const localExpiry = new Date(expires.getTime() - expires.getTimezoneOffset() * 60_000)
    .toISOString()
    .slice(0, 16);
  await page.getByLabel('basis expiry').fill(localExpiry);
  await page.getByRole('button', { name: 'Record basis' }).click();

  let basisRow = page.locator('tbody tr').filter({ hasText: 'marketing' });
  await expect(basisRow.getByText('active', { exact: true })).toBeVisible();
  await expect(basisRow).toContainText('in 1d');

  // The backend supplies `active` as an as-of-now field. Exercise the browser's
  // expired branch explicitly without sleeping until tomorrow: granted remains true
  // as historical evidence while active becomes false.
  await page.route(`**/v1/consent?subject=${encodeURIComponent(subject)}`, (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        consents: [
          {
            subject,
            purpose: 'marketing',
            granted: true,
            active: false,
            basis: 'consent',
            granted_at: new Date(Date.now() - 172_800_000).toISOString(),
            expires_at: new Date(Date.now() - 86_400_000).toISOString(),
            updated_by: 'test'
          }
        ]
      })
    })
  );
  await page.reload();
  basisRow = page.locator('tbody tr').filter({ hasText: 'marketing' });
  await expect(basisRow.getByText('expired', { exact: true })).toBeVisible();
  await expect(basisRow.getByRole('button', { name: 'Withdraw' })).toHaveCount(0);
});

test('connector creation fails closed when the catalog is unavailable', async ({ page }) => {
  await page.route('**/v1/context/connectors/catalog', (route) =>
    route.fulfill({
      status: 500,
      contentType: 'application/json',
      body: '{"error":"catalog registry offline"}'
    })
  );

  await page.goto('/data');
  await expect(page.getByTestId('connector-catalog-error')).toContainText(
    'catalog registry offline'
  );
  await expect(page.getByLabel('connector type').locator('option')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Define connector' })).toBeDisabled();
});

test('browses an entity and its event timeline', async ({ page, request }) => {
  const id = 'cust-' + uniq();
  // Seed an entity + a custom event via the API.
  await request.post('/v1/context/entities', {
    headers: { 'X-Api-Key': KEY },
    data: { entity_type: 'customer', entity_id: id, attributes: { tier: 'gold' } }
  });
  await request.post('/v1/context/events', {
    headers: { 'X-Api-Key': KEY },
    data: {
      entity_type: 'customer',
      entity_id: id,
      event_name: 'login',
      data: { ip: '1.2.3.4', email: 'private@example.test' }
    }
  });

  await page.goto('/data');
  const row = page.locator('tbody tr').filter({ hasText: id });
  await expect(row).toBeVisible();
  await row.getByRole('link', { name: id }).click();

  // Entity detail shows the attribute and the event timeline.
  await expect(page.getByRole('heading', { level: 1 })).toContainText(id);
  await expect(page.getByText('gold')).toBeVisible();
  await expect(page.locator('.timeline')).toContainText('login');
  await expect(page.locator('.timeline')).toContainText('private@example.test');

  // The admin can protect the actual vault subject, deliberately release it, and
  // then fulfil an irreversible erasure request. The post-erasure reload must
  // replace already-rendered PII with the backend's crypto-shredded view.
  await page.getByLabel('legal hold reason').fill('active dispute');
  await page.getByRole('button', { name: 'Place legal hold' }).click();
  await expect(page.getByText('legal hold active')).toBeVisible();

  page.once('dialog', (dialog) => dialog.accept());
  await page.getByRole('button', { name: 'Release legal hold' }).click();
  await expect(page.getByText('eligible when obligations allow')).toBeVisible();

  await page.getByLabel('I understand that the protected data cannot be recovered.').check();
  page.once('dialog', (dialog) => dialog.accept());
  await page.getByRole('button', { name: 'Permanently erase protected data' }).click();
  await expect(page.getByText('crypto-shredded', { exact: true })).toBeVisible();
  await expect(page.locator('.timeline')).toContainText('[erased]');
  await expect(page.locator('.timeline')).not.toContainText('private@example.test');
});
