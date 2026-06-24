// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

function uniqueName(): string {
  return 'agent-' + Math.random().toString(36).slice(2, 8);
}

// The UI authenticates via the session cookie; sign the page context in first.
test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('defines an agent from the registry and shows the run summary', async ({ page }) => {
  await page.goto('/agents');
  await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible();

  // The "Define agent" form is a disclosure (content-first); open it.
  await page.getByText('+ Define agent').click();
  const name = uniqueName();
  await page.getByLabel('agent name').fill(name);
  await page.getByLabel('system prompt').fill('be terse');
  // Exercise the deepened form: a tool set and a structured-output schema.
  await page.getByLabel('tools').fill('bureau');
  await page.getByLabel('output schema').fill('{"type":"object","required":["risk"]}');
  await page.getByRole('button', { name: 'Define agent' }).click();

  // .first(): a reused dev server may carry agents from prior runs.
  await expect(page.getByRole('link', { name }).first()).toBeVisible();
  await expect(page.getByLabel('run summary')).toContainText('Runs');
  // The capability badges reflect the schema + tools just defined.
  const row = page.locator('tbody tr').filter({ hasText: name });
  await expect(row).toContainText('structured');
  await expect(row).toContainText('1 tool');
});

test('runs an agent and escalates the run to a case', async ({ page, request }) => {
  // Escalation confirms before opening a case — accept the dialog.
  page.on('dialog', (d) => d.accept());
  // Seed an agent through the API.
  const name = uniqueName();
  const created = await request.post('/v1/agents', {
    headers: { 'X-Api-Key': KEY },
    data: { name, system: 'assess' }
  });
  expect(created.ok()).toBeTruthy();

  await page.goto(`/agents/${name}`);
  await expect(page.getByLabel('prompt', { exact: true })).toBeVisible();

  await page.getByLabel('prompt', { exact: true }).fill('is this suspicious?');
  await page.getByRole('button', { name: 'Run', exact: true }).click();

  // The run appears in the log (the stub echoes the prompt) and run count updates.
  await expect(async () => {
    await page.getByRole('button', { name: 'Reload' }).click();
    await expect(page.getByTestId('runs').locator('li')).toHaveCount(1);
    await expect(page.getByTestId('run-count')).toHaveText('1');
  }).toPass({ timeout: 5000 });

  // Escalate the run; it opens a case (no UI error surfaces).
  await page
    .getByRole('button', { name: /escalate/i })
    .first()
    .click();
  await expect(page.locator('p.err')).toHaveCount(0);
});

test('streams an agent run over SSE in the browser', async ({ page, request }) => {
  const name = uniqueName();
  await request.post('/v1/agents', { headers: { 'X-Api-Key': KEY }, data: { name } });
  // Wait for the projection so the detail page loads the agent (Stream enabled).
  await expect(async () => {
    const r = await request.get(`/v1/agents/${name}`, { headers: { 'X-Api-Key': KEY } });
    expect(r.ok()).toBeTruthy();
  }).toPass({ timeout: 5000 });

  await page.goto(`/agents/${name}`);
  await page.getByLabel('stream prompt').fill('hello there');
  await page.getByLabel('transport').selectOption('sse');
  await page.getByRole('button', { name: 'Stream', exact: true }).click();

  // The output accumulates the streamed deltas (the stub echoes the prompt).
  await expect(page.getByTestId('stream-output')).toContainText('stub: hello there');
});
