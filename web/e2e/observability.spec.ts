// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';
const uniqueSlug = () => 'obs-' + Math.random().toString(36).slice(2, 9);

// The UI authenticates via the session cookie; sign the page context in first.
test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('shows AI usage and the SLO surface', async ({ page }) => {
  await page.goto('/observability');
  await expect(page.getByRole('heading', { name: 'Observability' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'AI usage & cost' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Service-level objectives' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Request tracing' })).toBeVisible();
});

test('sets and clears a per-flow SLO objective', async ({ page, request }) => {
  // Clearing an objective asks for confirmation — accept it.
  page.on('dialog', (d) => d.accept());
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'SLO Target' }
  });
  const { flow_id } = await created.json();
  await request.post(`/v1/flows/${flow_id}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input' },
          { id: 'out', type: 'output' }
        ],
        edges: [{ from: 'in', to: 'out' }]
      }
    }
  });

  await page.goto('/observability');
  const card = page.locator('.slo-card').filter({ hasText: 'SLO Target' });
  await expect(card).toBeVisible();

  // Open the editor (the no-objective state leads with a Set-objective CTA), set an
  // objective, then confirm attainment renders for the flow.
  await card.getByRole('button', { name: 'Set objective' }).click();
  await card.getByLabel('Success target %').fill('95');
  await card.getByLabel('Latency target ms').fill('2000');
  await card.getByRole('button', { name: 'Set objective' }).click();
  await expect(card.getByText(/target 95%/)).toBeVisible();

  // Clearing removes the objective and brings back the editor.
  await card.getByRole('button', { name: 'Clear objective' }).click();
  await expect(card.getByRole('button', { name: 'Set objective' })).toBeVisible();
});
