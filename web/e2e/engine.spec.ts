// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';
const uniqueSlug = () => 'ui-' + Math.random().toString(36).slice(2, 9);

test('lists and creates a flow', async ({ page }) => {
  const slug = uniqueSlug();
  await page.goto('/engine');
  await expect(page.getByRole('heading', { name: /Decision Engine/i })).toBeVisible();

  await page.getByLabel('slug').fill(slug);
  await page.getByLabel('name').fill('UI Flow');
  await page.getByRole('button', { name: 'Create flow' }).click();

  await expect(page.getByRole('link', { name: 'UI Flow' })).toBeVisible();
  await expect(page.getByText(slug)).toBeVisible();
});

test('renders a flow graph and runs a test decision', async ({ page, request }) => {
  const slug = uniqueSlug();

  // Seed a decideable flow version through the API.
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Seeded' }
  });
  expect(created.ok()).toBeTruthy();
  const { flow_id } = await created.json();

  const pub = await request.post(`/v1/flows/${flow_id}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input' },
          {
            id: 'a',
            type: 'assignment',
            config: { assignments: [{ target: 'decision', expr: "'SEEDED'" }] }
          },
          { id: 'out', type: 'output', config: { fields: ['decision'] } }
        ],
        edges: [
          { from: 'in', to: 'a' },
          { from: 'a', to: 'out' }
        ]
      }
    }
  });
  expect(pub.ok()).toBeTruthy();

  await page.goto(`/engine/${flow_id}`);

  // The graph renders on the Svelte Flow canvas (3 nodes). The assertion retries,
  // covering the async flow-registry projection catching up.
  await expect(page.locator('.svelte-flow__node')).toHaveCount(3);

  // Inline test run -> a completed decision.
  await page.getByLabel('input data').fill('{}');
  await page.getByRole('button', { name: 'Run' }).click();
  const result = page.getByTestId('run-result');
  await expect(result).toContainText('"status": "completed"');
  await expect(result).toContainText('SEEDED');
});
