// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

// Both tests drive the same tenant's policy list and discussion threads; run them
// serially so they don't race each other's global-list/thread state under the
// suite's fullyParallel default.
test.describe.configure({ mode: 'serial' });

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('creates a policy bound to a flow and publishes a band', async ({ page, request }) => {
  page.on('dialog', (d) => d.accept()); // publishing a policy version now asks to confirm
  const slug = 'pol-' + Math.random().toString(36).slice(2, 8);
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: `PolFlow ${slug}` }
  });
  const { flow_id } = await created.json();
  // Publish a score-passthrough version so the disposition backtest can run.
  await request.post(`/v1/flows/${flow_id}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input' },
          {
            id: 'a',
            type: 'assignment',
            config: { assignments: [{ target: 'score', expr: 'score' }] }
          },
          { id: 'out', type: 'output', config: { fields: ['score'] } }
        ],
        edges: [
          { from: 'in', to: 'a' },
          { from: 'a', to: 'out' }
        ]
      }
    }
  });

  await page.goto('/policies');
  await expect(page.getByRole('heading', { name: 'Policies' })).toBeVisible();

  await page.getByLabel('policy name').fill(`stp-${slug}`);
  await page.getByLabel('flow', { exact: true }).selectOption(slug);
  await page.getByRole('button', { name: /Create policy/ }).click();

  // The band editor opens for the new policy; add an auto-approve band and publish.
  const editor = page.getByTestId('band-editor');
  await expect(editor).toBeVisible();
  await page.getByRole('button', { name: 'Add band' }).click();
  await page.getByLabel('band 0 when').fill('score >= 0.85');
  await page.getByLabel('band 0 disposition').selectOption('approve');

  // The plain-language band preview reflects the rule + the default, in order.
  const bandPreview = page.getByTestId('band-preview');
  await expect(bandPreview).toContainText('if score >= 0.85 → approve');
  await expect(bandPreview).toContainText('otherwise → refer');

  await page.getByTestId('publish-policy').click();

  await expect(page.getByText(/Published policy v1/)).toBeVisible();
  // The list now shows the published version.
  await expect(page.locator('tr', { hasText: `stp-${slug}` })).toContainText('v1');

  // Loosen the band to 0.4 and preview the impact vs the published v1: the 0.5
  // row flips from refer to approve.
  await page.getByLabel('band 0 when').fill('score >= 0.4');
  await page.getByLabel('backtest dataset').fill('[{"score": 0.9}, {"score": 0.5}]');
  await page.getByTestId('backtest-policy').click();
  await expect(page.getByTestId('backtest-result')).toBeVisible();
  await expect(page.getByText(/would change disposition/)).toBeVisible();

  // The policy carries a discussion thread — post an explanation, then a threaded reply.
  const thread = page.getByTestId('comment-thread');
  await thread.getByLabel('new comment').fill('Loosening the cutoff for Q3 volume.');
  await thread.getByTestId('post-comment').click();
  await expect(thread).toContainText('Loosening the cutoff for Q3 volume.');

  await thread.getByRole('button', { name: 'Reply' }).click();
  await thread.getByLabel('new comment').fill('Approved by risk for the quarter.');
  await thread.getByTestId('post-comment').click();
  await expect(thread.locator('.reply')).toContainText('Approved by risk for the quarter.');
});

test('hands a published policy version from maker to approver', async ({ page, request }) => {
  const suffix = Math.random().toString(36).slice(2, 9);
  const name = `Handoff policy ${suffix}`;
  const makerActor = `policy-maker-${suffix}`;
  const keyResponse = await request.post('/v1/api-keys', {
    headers: { 'X-Api-Key': KEY },
    data: { name: makerActor, actor: makerActor, role: 'editor', scope: '*' }
  });
  expect(keyResponse.ok()).toBeTruthy();
  const { secret: makerKey } = await keyResponse.json();
  const makerHeaders = { 'X-Api-Key': makerKey };

  const flow = await request.post('/v1/flows', {
    headers: makerHeaders,
    data: { slug: `policy-handoff-${suffix}`, name: `Policy handoff ${suffix}` }
  });
  expect(flow.ok()).toBeTruthy();
  const policy = await request.post('/v1/policies', {
    headers: makerHeaders,
    data: { name, flow_slug: `policy-handoff-${suffix}` }
  });
  expect(policy.ok()).toBeTruthy();
  const { policy_id } = await policy.json();
  const published = await request.post(`/v1/policies/${policy_id}/versions`, {
    headers: makerHeaders,
    data: {
      spec: {
        rules: [{ when: 'risk < 30', disposition: 'approve', code: 'LOW_RISK' }],
        default: 'refer'
      }
    }
  });
  expect(published.ok()).toBeTruthy();

  // The maker sees the unpublished-to-production state and raises the work item
  // from the policy workspace.
  await page.context().clearCookies();
  expect(
    (
      await page.context().request.post('/v1/login', {
        data: { api_key: makerKey }
      })
    ).ok()
  ).toBeTruthy();
  await page.goto(`/policies?governance=${encodeURIComponent(policy_id)}#policy-governance`);
  const makerGovernance = page.getByTestId('policy-governance');
  await expect(makerGovernance).toContainText('no approved version');
  await makerGovernance.getByTestId('request-policy-approval').click();
  await expect(makerGovernance).toContainText('pending review');

  // The separate checker receives an unambiguous, actionable notification that
  // deep-links to this exact policy and records the approval reason.
  await page.context().clearCookies();
  expect(
    (
      await page.context().request.post('/v1/login', {
        data: { api_key: KEY }
      })
    ).ok()
  ).toBeTruthy();
  await page.goto('/');
  const bell = page.getByTestId('notifications-bell');
  await bell.locator('summary').click();
  const approval = bell.locator('button.item').filter({
    hasText: `Approval requested: policy ${name} v1`
  });
  await expect(approval).toBeVisible();
  await approval.click();
  await expect(page).toHaveURL(
    (url) =>
      url.pathname === '/policies' &&
      url.searchParams.get('governance') === policy_id &&
      url.hash === '#policy-governance'
  );

  const checkerGovernance = page.getByTestId('policy-governance');
  await checkerGovernance.getByRole('button', { name: 'Approve' }).click();
  await checkerGovernance
    .getByLabel('policy decision reason')
    .fill('Independent threshold and impact review passed.');
  await checkerGovernance.getByRole('button', { name: 'Confirm approval' }).click();
  await expect(checkerGovernance).toContainText('approved v1');
  await expect(checkerGovernance).toContainText('serving in staging and production');
});

test('switching policies reloads the discussion thread (not the prior policy)', async ({
  page,
  request
}) => {
  const slug = 'pol2-' + Math.random().toString(36).slice(2, 8);
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: `PolFlow2 ${slug}` }
  });
  const { flow_id } = await created.json();
  await request.post(`/v1/flows/${flow_id}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input' },
          { id: 'out', type: 'output', config: { fields: ['score'] } }
        ],
        edges: [{ from: 'in', to: 'out' }]
      }
    }
  });

  await page.goto('/policies');

  // First policy: post a comment that must NOT bleed into a different policy.
  await page.getByLabel('policy name').fill(`alpha-${slug}`);
  await page.getByLabel('flow', { exact: true }).selectOption(slug);
  await page.getByRole('button', { name: /Create policy/ }).click();
  await expect(page.getByTestId('band-editor')).toBeVisible();
  const thread = page.getByTestId('comment-thread');
  await thread.getByLabel('new comment').fill('Only-on-alpha note.');
  await thread.getByTestId('post-comment').click();
  await expect(thread).toContainText('Only-on-alpha note.');

  // Second policy: creating it selects it; the keyed thread remounts and loads the
  // (empty) second policy's comments rather than showing the first's.
  await page.getByLabel('policy name').fill(`beta-${slug}`);
  await page.getByLabel('flow', { exact: true }).selectOption(slug);
  await page.getByRole('button', { name: /Create policy/ }).click();
  await expect(page.getByTestId('band-editor')).toContainText(`beta-${slug}`);
  await expect(thread).not.toContainText('Only-on-alpha note.');

  // Switching back to the first policy shows its comment again.
  await page
    .locator('tr', { hasText: `alpha-${slug}` })
    .getByRole('button', { name: 'Edit bands' })
    .click();
  await expect(thread).toContainText('Only-on-alpha note.');
});

test('policy sample dataset exercises every band', async ({ page, request }) => {
  const slug = 'pol3-' + Math.random().toString(36).slice(2, 8);
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'PolicySamples' }
  });
  const { flow_id } = await created.json();
  await request.post(`/v1/flows/${flow_id}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input', position: { x: 0, y: 0 } },
          {
            id: 'a',
            type: 'assignment',
            config: { assignments: [{ target: 'risk', expr: 'score / 10' }] },
            position: { x: 200, y: 0 }
          },
          { id: 'out', type: 'output', position: { x: 400, y: 0 } }
        ],
        edges: [
          { from: 'in', to: 'a' },
          { from: 'a', to: 'out' }
        ]
      }
    }
  });

  await page.goto('/policies');
  await page.getByPlaceholder('policy name').fill('Sample Policy');
  await page.getByLabel('flow', { exact: true }).selectOption({ label: `PolicySamples (${slug})` });
  await page.getByRole('button', { name: 'Create policy' }).click();
  await page.getByRole('button', { name: 'Edit bands' }).first().click();
  await page.getByRole('button', { name: 'Add band' }).click();
  await page.getByPlaceholder('when (expr over output)').first().fill('risk < 35');

  await page.getByTestId('sample-impact').click();
  const rows = JSON.parse(await page.getByLabel('backtest dataset').inputValue());
  // risk < 35 → rows below, at, and above the 35 threshold.
  const risks = rows.map((r: { risk: number }) => r.risk);
  expect(risks.some((v: number) => v < 35)).toBe(true);
  expect(risks).toContain(35);
  expect(risks.some((v: number) => v > 35)).toBe(true);
});
