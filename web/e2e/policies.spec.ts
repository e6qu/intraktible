// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('creates a policy bound to a flow and publishes a band', async ({ page, request }) => {
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
