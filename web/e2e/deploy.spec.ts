// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';
const uniqueSlug = () => 'ui-' + Math.random().toString(36).slice(2, 9);

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

const constGraph = (expr: string) => ({
  nodes: [
    { id: 'in', type: 'input' },
    { id: 'a', type: 'assignment', config: { assignments: [{ target: 'decision', expr }] } },
    { id: 'out', type: 'output', config: { fields: ['decision'] } }
  ],
  edges: [
    { from: 'in', to: 'a' },
    { from: 'a', to: 'out' }
  ]
});

test('deploys to sandbox and runs the production four-eyes flow', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Deployable' }
  });
  const { flow_id } = await created.json();
  for (const expr of ["'A'", "'B'"]) {
    const pub = await request.post(`/v1/flows/${flow_id}/versions`, {
      headers: { 'X-Api-Key': KEY },
      data: { graph: constGraph(expr) }
    });
    expect(pub.ok()).toBeTruthy();
  }

  await page.goto(`/engine/${flow_id}`);
  const panel = page.getByTestId('deploy-panel');
  await expect(panel).toBeVisible();

  // Deploy v1 to sandbox (no approval needed) -> the live badge updates.
  await page.getByLabel('deploy version').fill('1');
  await page.getByLabel('deploy environment').selectOption('sandbox');
  await page.getByTestId('deploy-submit').click();
  await expect(panel.getByText(/sandbox:/)).toContainText('v1');

  // Propose v2 to production -> a maker-checker request appears.
  await page.getByLabel('deploy version').fill('2');
  await page.getByLabel('deploy environment').selectOption('production');
  await page.getByTestId('deploy-submit').click();
  const requests = page.getByTestId('pending-requests');
  await expect(requests).toBeVisible();
  await expect(requests.locator('tbody tr')).toHaveCount(1);
  await expect(requests).toContainText('v2');

  // Approving your own request is blocked by four-eyes (the proposer is the dev user).
  await requests.getByRole('button', { name: 'Approve' }).click();
  await expect(page.locator('.err')).toContainText('four-eyes');
});
