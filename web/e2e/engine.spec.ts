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

  // .first(): a reused dev server may carry flows named "UI Flow" from prior
  // runs; the unique slug below pins down the one this test created.
  await expect(page.getByRole('link', { name: 'UI Flow' }).first()).toBeVisible();
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

test('builds a flow in the editor and publishes it', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Built' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  // Wait for the editor to be interactive before driving it (the page loads the
  // flow on mount; under parallel load the dev server can be slow to hydrate).
  await expect(page.getByLabel('new node type')).toBeVisible();

  // Add input (n1), assignment (n2), output (n3) via the palette.
  for (const type of ['input', 'assignment', 'output']) {
    await page.getByLabel('new node type').selectOption(type);
    await page.getByRole('button', { name: 'Add', exact: true }).click();
  }
  // Barrier: all three nodes (and thus the edge-select options) are rendered
  // before we wire edges, so selectOption can't race the render.
  await expect(page.locator('aside ul.nodes li')).toHaveCount(3);

  // Configure the assignment and output nodes.
  await page.locator('aside ul.nodes button.link').filter({ hasText: 'n2' }).click();
  await page
    .getByLabel('node config')
    .fill('{"assignments":[{"target":"decision","expr":"\'BUILT\'"}]}');
  await page.locator('aside ul.nodes button.link').filter({ hasText: 'n3' }).click();
  await page.getByLabel('node config').fill('{"fields":["decision"]}');

  // Wire in -> assignment -> output. Match the select labels exactly: Svelte Flow
  // renders each edge with a default aria-label "Edge from <a> to <b>", which a
  // substring getByLabel('edge from') would also match once an edge is on the
  // canvas (a render race) — exact:true pins the locator to the form control.
  await page.getByLabel('edge from', { exact: true }).selectOption('n1');
  await page.getByLabel('edge to', { exact: true }).selectOption('n2');
  await page.getByRole('button', { name: 'Add edge' }).click();
  await page.getByLabel('edge from', { exact: true }).selectOption('n2');
  await page.getByLabel('edge to', { exact: true }).selectOption('n3');
  await page.getByRole('button', { name: 'Add edge' }).click();

  // Publish -> v1, and the canvas now shows the three nodes.
  await page.getByRole('button', { name: 'Publish version' }).click();
  await expect(page.getByText('Published v1')).toBeVisible();
  await expect(page.locator('.svelte-flow__node')).toHaveCount(3);

  // The built flow decides.
  await page.getByLabel('input data').fill('{}');
  await page.getByRole('button', { name: 'Run' }).click();
  await expect(page.getByTestId('run-result')).toContainText('BUILT');
});

test('a structured config panel edits a node without raw JSON', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Structured' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  await expect(page.getByLabel('new node type')).toBeVisible();

  // Add a split node (auto-selected) and set its condition via the structured
  // field — no JSON typing. The advanced JSON view reflects the same config.
  await page.getByLabel('new node type').selectOption('split');
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await page.getByLabel('condition', { exact: true }).fill('score >= 700');
  await expect(page.getByLabel('node config')).toHaveValue('{"condition":"score >= 700"}');
});

test('the assignment panel edits target/expr rows without raw JSON', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Assign' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  await expect(page.getByLabel('new node type')).toBeVisible();

  await page.getByLabel('new node type').selectOption('assignment');
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await page.getByRole('button', { name: 'Add assignment' }).click();
  await page.getByLabel('assignment 0 target').fill('decision');
  await page.getByLabel('assignment 0 expr').fill("'APPROVE'");
  await expect(page.getByLabel('node config')).toHaveValue(
    '{"assignments":[{"target":"decision","expr":"\'APPROVE\'"}]}'
  );
});

test('a scorecard panel edits factors and output without raw JSON', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Score' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  await expect(page.getByLabel('new node type')).toBeVisible();

  await page.getByLabel('new node type').selectOption('scorecard');
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await page.getByLabel('scorecard output').fill('risk');
  await page.getByRole('button', { name: 'Add factor' }).click();
  await page.getByLabel('factor 0 when').fill('fico < 600');
  await page.getByLabel('factor 0 weight').fill('25');

  // The structured edits round-trip into the advanced JSON view.
  await expect(page.getByLabel('node config')).toHaveValue(
    '{"output":"risk","factors":[{"when":"fico < 600","weight":25}]}'
  );
});

test('a rule panel edits when/then clauses without raw JSON', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Rule' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  await expect(page.getByLabel('new node type')).toBeVisible();

  await page.getByLabel('new node type').selectOption('rule');
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await page.getByRole('button', { name: 'Add rule' }).click();
  await page.getByLabel('rule 0 when').fill('amount > 1000');
  await page.getByRole('button', { name: 'Add then' }).click();
  await page.getByLabel('rule 0 then 0 target').fill('flag');
  await page.getByLabel('rule 0 then 0 expr').fill("'high'");

  await expect(page.getByLabel('node config')).toHaveValue(
    '{"rules":[{"when":"amount > 1000","then":[{"target":"flag","expr":"\'high\'"}]}]}'
  );
});

test('shows the backend validation error when publishing an invalid graph', async ({
  page,
  request
}) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Invalid' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  // A single rule node (no input/output) must be rejected loudly by the backend.
  await page.getByLabel('new node type').selectOption('rule');
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await page.getByRole('button', { name: 'Publish version' }).click();
  await expect(page.locator('.err')).toContainText('input');
});
