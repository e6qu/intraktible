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
  // The canvas is the primary surface; deploy/versions live behind their tab.
  await expect(page.getByTestId('flow-canvas')).toBeVisible();
  await page.getByTestId('tab-deploy').click();
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
  // Promotion now confirms first (a high-stakes action); accept and assert the prompt.
  page.once('dialog', (d) => {
    expect(d.message()).toContain('staging');
    void d.accept();
  });
  await page.getByTestId('promote-submit').click();
  await expect(panel.getByText(/staging:/)).toContainText('v1');

  // Propose v2 to production -> a maker-checker request appears.
  await page.getByLabel('deploy version').fill('2');
  await page.getByLabel('deploy environment').selectOption('production');
  await page.getByTestId('deploy-submit').click();
  const requests = page.getByTestId('deployment-requests');
  await expect(requests).toBeVisible();
  await expect(requests.locator('tbody tr:not(.threadrow)')).toHaveCount(1);
  await expect(requests).toContainText('v2');
  await expect(requests.locator('.reqstatus')).toHaveText('pending');

  // The request carries a comment thread — post an explanation and see it appear.
  const thread = requests.getByTestId('comment-thread');
  await thread.getByLabel('new comment').fill('Holding until the backtest passes.');
  await thread.getByTestId('post-comment').click();
  await expect(thread).toContainText('Holding until the backtest passes.');

  // Four-eyes: the requester cannot approve their own deploy. The UI disables Approve
  // with an explanatory tooltip (the server also rejects it as defence in depth).
  const approve = requests.getByRole('button', { name: 'Approve' });
  await expect(approve).toBeDisabled();
  await expect(approve).toHaveAttribute('title', /four-eyes/i);

  // The flow list now surfaces the sandbox deployment for this flow.
  await page.goto('/engine');
  const flowRow = page.locator('tbody tr').filter({ hasText: slug });
  await expect(flowRow).toContainText('v1');
});

test('hands a scheduled production deployment from maker to approver', async ({
  page,
  request
}) => {
  const suffix = Math.random().toString(36).slice(2, 9);
  const makerActor = `deploy-maker-${suffix}`;
  const keyResponse = await request.post('/v1/api-keys', {
    headers: { 'X-Api-Key': KEY },
    data: { name: makerActor, actor: makerActor, role: 'editor', scope: '*' }
  });
  expect(keyResponse.ok()).toBeTruthy();
  const { secret: makerKey } = await keyResponse.json();
  const makerHeaders = { 'X-Api-Key': makerKey };

  const created = await request.post('/v1/flows', {
    headers: makerHeaders,
    data: { slug: uniqueSlug(), name: `Scheduled handoff ${suffix}` }
  });
  expect(created.ok()).toBeTruthy();
  const { flow_id } = await created.json();
  for (const expr of ["'baseline'", "'window'"]) {
    const published = await request.post(`/v1/flows/${flow_id}/versions`, {
      headers: makerHeaders,
      data: { graph: constGraph(expr) }
    });
    expect(published.ok()).toBeTruthy();
  }

  // Work as the maker in the actual console and propose the production window.
  await page.context().clearCookies();
  const makerLogin = await page.context().request.post('/v1/login', {
    data: { api_key: makerKey }
  });
  expect(makerLogin.ok()).toBeTruthy();
  await page.goto(`/engine/${flow_id}?tab=deploy`);
  const schedules = page.getByTestId('schedules-panel');
  await schedules.locator('summary').click();
  await schedules.getByLabel('schedule environment').selectOption('production');
  await schedules.getByLabel('schedule version').fill('2');
  const future = new Date(Date.now() + 24 * 60 * 60 * 1000);
  const localFuture = new Date(future.getTime() - future.getTimezoneOffset() * 60_000)
    .toISOString()
    .slice(0, 16);
  await schedules.getByLabel('schedule at').fill(localFuture);
  await schedules.getByRole('button', { name: 'Schedule' }).click();

  const requests = page.getByTestId('deployment-requests');
  await expect(requests).toContainText('scheduled');
  await expect(requests).toContainText('pending');
  await expect(requests.getByRole('button', { name: 'Approve' })).toBeDisabled();
  await expect(schedules.locator('summary')).toContainText('(0)');

  // Change actor. The approver receives actionable work in the shared inbox and
  // its notification opens the exact review tab, not the unrelated canvas.
  await page.context().clearCookies();
  const approverLogin = await page.context().request.post('/v1/login', {
    data: { api_key: KEY }
  });
  expect(approverLogin.ok()).toBeTruthy();
  await page.goto('/');
  const bell = page.getByTestId('notifications-bell');
  await expect(bell.getByTestId('notif-badge')).toBeVisible();
  await bell.locator('summary').click();
  const approval = bell
    .locator('button.item')
    .filter({
      hasText: 'Approval requested: schedule v2 to production'
    })
    .first();
  await expect(approval).toBeVisible();
  await approval.click();
  await expect(page).toHaveURL(
    (url) => url.pathname === `/engine/${flow_id}` && url.searchParams.get('tab') === 'deploy'
  );

  const approverRequests = page.getByTestId('deployment-requests');
  await approverRequests.getByRole('button', { name: 'Approve' }).click();
  await page.getByLabel('decision reason').fill('Independent maintenance-window review passed.');
  await approverRequests.getByRole('button', { name: 'Confirm approve' }).click();
  await expect(approverRequests.locator('.reqstatus')).toHaveText('approved');
  await expect(approverRequests).toContainText('Independent maintenance-window review passed.');

  // Approval creates a pending schedule; it does not deploy before the requested
  // time. The scheduler transition itself is covered against the real command and
  // replay path by the Go lifecycle regression.
  const approvedSchedules = page.getByTestId('schedules-panel');
  await approvedSchedules.locator('summary').click();
  await expect(approvedSchedules.locator('summary')).toContainText('(1)');
  await expect(approvedSchedules.locator('li')).toContainText('production v2');
  await expect(approvedSchedules.locator('li .reqstatus')).toHaveText('pending');
  await expect(page.getByTestId('deploy-panel').getByText(/production:/)).not.toContainText('v2');
});
