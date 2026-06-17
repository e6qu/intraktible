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

  // The version-diff (v1 vs v2) shows node 'a' changed and nothing else — the two
  // graphs differ only in the assignment node's expression.
  const vdiff = page.getByTestId('version-diff');
  await expect(vdiff.locator('li')).toHaveCount(1);
  await expect(vdiff).toContainText('node');
  await expect(vdiff).toContainText('a');

  // Deploy v1 to sandbox (no approval needed) -> the live badge updates.
  await page.getByLabel('deploy version').fill('1');
  await page.getByLabel('deploy environment').selectOption('sandbox');
  await page.getByTestId('deploy-submit').click();
  await expect(panel.getByText(/sandbox:/)).toContainText('v1');

  // Promote sandbox -> staging (a non-prod target deploys directly).
  await page.getByLabel('promote from').selectOption('sandbox');
  await page.getByLabel('promote to').selectOption('staging');
  await page.getByTestId('promote-submit').click();
  await expect(panel.getByText(/staging:/)).toContainText('v1');

  // Propose v2 to production -> a maker-checker request appears.
  await page.getByLabel('deploy version').fill('2');
  await page.getByLabel('deploy environment').selectOption('production');
  await page.getByTestId('deploy-submit').click();
  const requests = page.getByTestId('pending-requests');
  await expect(requests).toBeVisible();
  await expect(requests.locator('tbody tr:not(.threadrow)')).toHaveCount(1);
  await expect(requests).toContainText('v2');

  // The request carries a comment thread — post an explanation and see it appear.
  const thread = requests.getByTestId('comment-thread');
  await thread.getByLabel('new comment').fill('Holding until the backtest passes.');
  await thread.getByTestId('post-comment').click();
  await expect(thread).toContainText('Holding until the backtest passes.');

  // Approving your own request is blocked by four-eyes (the proposer is the dev user).
  await requests.getByRole('button', { name: 'Approve' }).click();
  await expect(page.locator('.err')).toContainText('four-eyes');

  // The flow list now surfaces the sandbox deployment for this flow.
  await page.goto('/engine');
  const flowRow = page.locator('tbody tr').filter({ hasText: slug });
  await expect(flowRow).toContainText('v1');
});
