// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';
const uniqueSlug = () => 'dec-' + Math.random().toString(36).slice(2, 8);

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('a decision run shows in the history and its detail has the node trace', async ({
  page,
  request
}) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Hist Flow' }
  });
  const { flow_id } = await created.json();
  await request.post(`/v1/flows/${flow_id}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input' },
          { id: 'a', type: 'assignment', config: { assignments: [{ target: 'd', expr: "'OK'" }] } },
          { id: 'out', type: 'output', config: { fields: ['d'] } }
        ],
        edges: [
          { from: 'in', to: 'a' },
          { from: 'a', to: 'out' }
        ]
      }
    }
  });

  // Decide (retry until the flow projection is live), capturing the decision id.
  let decisionId = '';
  await expect(async () => {
    const r = await request.post(`/v1/flows/${slug}/production/decide`, {
      headers: { 'X-Api-Key': KEY },
      data: { data: {} }
    });
    const body = await r.json();
    expect(body.status).toBe('completed');
    decisionId = body.decision_id;
  }).toPass({ timeout: 5000 });

  // It appears in the history list.
  await page.goto('/decisions');
  const row = page.locator('tr', { hasText: slug }).first();
  await expect(row).toBeVisible();
  await expect(row.getByText('completed')).toBeVisible();

  // The detail page shows the node trace.
  await page.goto(`/decisions/${decisionId}`);
  await expect(page.getByRole('heading', { name: new RegExp(slug) })).toBeVisible();
  await expect(page.locator('ol.trace')).toContainText('assignment');
  await expect(page.getByRole('button', { name: 'Sequence', exact: true })).toBeVisible();
});
