// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';
const uniqueSlug = () => 'ui-' + Math.random().toString(36).slice(2, 9);

// The UI authenticates via the session cookie; sign the page context in first.
test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

// The tools panel starts closed (the icon rail is the primary add path on the
// board); these tests drive the typed add/edge flow, so open it explicitly and
// use its select as the hydration marker.
async function openTools(page: import('@playwright/test').Page) {
  await page.getByTestId('toggle-panel').click();
  await expect(page.getByLabel('new node type')).toBeVisible();
}

test('lists and creates a flow', async ({ page }) => {
  const slug = uniqueSlug();
  await page.goto('/engine');
  await expect(page.getByRole('heading', { name: /Flows/i })).toBeVisible();

  await page.getByLabel('slug').fill(slug);
  await page.getByLabel('name').fill('UI Flow');
  await page.getByRole('button', { name: 'Create flow' }).click();

  // Creating a flow now navigates straight into its builder; return to the list to
  // confirm it's there and undeployed.
  await expect(page).toHaveURL(/\/engine\/.+/);
  await page.goto('/engine');

  // .first(): a reused dev server may carry flows named "UI Flow" from prior
  // runs; the unique slug below pins down the one this test created.
  await expect(page.getByRole('link', { name: 'UI Flow' }).first()).toBeVisible();
  await expect(page.getByText(slug)).toBeVisible();
  // A brand-new flow is undeployed everywhere — the columns say so explicitly.
  const row = page.locator('tbody tr').filter({ hasText: slug });
  await expect(row.getByText('not deployed').first()).toBeVisible();
});

test('organizes the builder into tabs with the canvas pinned', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Tabbed' }
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

  await page.goto(`/engine/${flow_id}`);
  const canvas = page.getByTestId('flow-canvas');
  await expect(canvas).toBeVisible();

  // Default tab is Test & analyze: the test-run heading is visible; deploy is not
  // even in the DOM (it lives behind its tab).
  await expect(page.getByRole('heading', { name: 'Test run' })).toBeVisible();
  await expect(page.getByTestId('deploy-panel')).toHaveCount(0);

  // Switching tabs swaps the panel while the canvas stays pinned at the top.
  await page.getByTestId('tab-deploy').click();
  await expect(page.getByTestId('deploy-panel')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Test run' })).toHaveCount(0);
  await expect(canvas).toBeVisible();

  await page.getByTestId('tab-monitor').click();
  await expect(page.getByTestId('monitors-panel')).toBeVisible();
  await expect(canvas).toBeVisible();
});

test('the test-run input guards invalid JSON and prefills a schema sample', async ({
  page,
  request
}) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Guarded' }
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
      },
      input_schema: {
        type: 'object',
        properties: { score: { type: 'number' }, name: { type: 'string' } }
      }
    }
  });

  await page.goto(`/engine/${flow_id}`);
  const data = page.getByLabel('input data');
  await data.fill('{ not json');
  await expect(page.getByText('Not valid JSON')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Run', exact: true })).toBeDisabled();

  // "Sample input" prefills a valid skeleton from the flow's input schema.
  await page.getByRole('button', { name: 'Sample input' }).click();
  await expect(page.getByText('Not valid JSON')).toHaveCount(0);
  await expect(data).toHaveValue(/"score": 1/);
  await expect(page.getByRole('button', { name: 'Run', exact: true })).toBeEnabled();
});

test('runs a what-if sensitivity sweep from the builder', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'What If UI' }
  });
  const { flow_id } = await created.json();
  await request.post(`/v1/flows/${flow_id}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input' },
          {
            id: 'a',
            type: 'assignment',
            config: { assignments: [{ target: 'decision', expr: `score > 5 ? "A":"B"` }] }
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

  await page.goto(`/engine/${flow_id}`);
  await page.getByLabel('whatif field').fill('score');
  await page.getByLabel('whatif values').fill('1, 3, 7, 9');
  await page.getByTestId('run-whatif').click();

  // The sweep flips from B to A once score crosses 5 — one transition.
  await expect(page.getByTestId('whatif-summary')).toContainText('1 transition');
  const rows = page.getByTestId('whatif-table').locator('tbody tr');
  await expect(rows).toHaveCount(4);
  await expect(rows.first()).toContainText('B');
  await expect(rows.last()).toContainText('A');
});

test('imports a flow from an exported document', async ({ page }) => {
  const slug = uniqueSlug();
  const doc = JSON.stringify({
    slug,
    name: 'Imported Flow',
    graph: {
      nodes: [
        { id: 'in', type: 'input' },
        { id: 'out', type: 'output' }
      ],
      edges: [{ from: 'in', to: 'out' }]
    }
  });

  await page.goto('/engine');
  await page.getByTestId('import-flow').locator('summary').click();
  await page.getByLabel('flow document').fill(doc);
  await page.getByTestId('import-submit').click();

  // Import navigates straight into the new flow's builder.
  await expect(page).toHaveURL(/\/engine\/[a-f0-9]+$/);

  // Re-importing the same document is a no-op (no new version).
  await page.goto('/engine');
  await page.getByTestId('import-flow').locator('summary').click();
  await page.getByLabel('flow document').fill(doc);
  await page.getByTestId('import-submit').click();
  await expect(page.getByText(/already at v1 — no change/)).toBeVisible();
});

test('assigns a shadow version from the builder', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Shadow UI' }
  });
  const { flow_id } = await created.json();
  const graph = (decision: string) => ({
    nodes: [
      { id: 'in', type: 'input' },
      {
        id: 'a',
        type: 'assignment',
        config: { assignments: [{ target: 'decision', expr: `'${decision}'` }] }
      },
      { id: 'out', type: 'output', config: { fields: ['decision'] } }
    ],
    edges: [
      { from: 'in', to: 'a' },
      { from: 'a', to: 'out' }
    ]
  });
  for (const d of ['A', 'B']) {
    await request.post(`/v1/flows/${flow_id}/versions`, {
      headers: { 'X-Api-Key': KEY },
      data: { graph: graph(d) }
    });
  }
  await request.post(`/v1/flows/${flow_id}/deployments`, {
    headers: { 'X-Api-Key': KEY },
    data: { environment: 'sandbox', version: 1 }
  });

  await page.goto(`/engine/${flow_id}`);
  await page.getByTestId('tab-deploy').click(); // shadow lives under Deploy & versions
  await page.getByTestId('shadow-panel').locator('summary').click();
  await page.getByLabel('shadow version for sandbox').selectOption('2');
  await expect(page.getByText('Shadowing v2 in sandbox')).toBeVisible();

  // The assignment round-trips: reloading rehydrates v2 as the sandbox shadow.
  await page.reload();
  await page.getByTestId('tab-deploy').click(); // reload resets to the default tab
  await page.getByTestId('shadow-panel').locator('summary').click();
  await expect(page.getByLabel('shadow version for sandbox')).toHaveValue('2');
});

test('imports a bundle of flows', async ({ page }) => {
  const graph = {
    nodes: [
      { id: 'in', type: 'input' },
      { id: 'out', type: 'output' }
    ],
    edges: [{ from: 'in', to: 'out' }]
  };
  const a = uniqueSlug();
  const b = uniqueSlug();
  const bundle = JSON.stringify({
    flows: [
      { slug: a, name: 'Bundle A', graph },
      { slug: b, name: 'Bundle B', graph }
    ]
  });

  await page.goto('/engine');
  await page.getByTestId('import-flow').locator('summary').click();
  await page.getByLabel('flow document').fill(bundle);
  await page.getByTestId('import-submit').click();

  // A bundle reports a summary (and stays on the list) rather than navigating.
  await expect(page.getByText(/Bundle: 2 published/)).toBeVisible();
  await expect(page.getByText(b)).toBeVisible();
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
  // Typed node cards show the node type + a config summary (not a bare box).
  const canvas = page.getByTestId('flow-canvas');
  await expect(canvas).toContainText('assignment');

  // Inline test run -> a completed decision.
  await page.getByLabel('input data').fill('{}');
  await page.getByRole('button', { name: 'Run', exact: true }).click();
  const result = page.getByTestId('run-result');
  await expect(result).toContainText('"status": "completed"');
  await expect(result).toContainText('SEEDED');
  // The run paints each node's last output onto its card (live status).
  await expect(canvas).toContainText('decision: SEEDED');
});

test('switches the flow canvas between card and BPMN views', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Process View' }
  });
  expect(created.ok()).toBeTruthy();
  const { flow_id } = await created.json();

  const pub = await request.post(`/v1/flows/${flow_id}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input', name: 'Start' },
          {
            id: 'gate',
            type: 'split',
            name: 'Route',
            config: { condition: 'true' }
          },
          { id: 'out', type: 'output', name: 'Finish' }
        ],
        edges: [
          { from: 'in', to: 'gate' },
          { from: 'gate', to: 'out', branch: 'yes' },
          { from: 'gate', to: 'out', branch: 'no' }
        ]
      }
    }
  });
  expect(pub.ok()).toBeTruthy();

  await page.goto(`/engine/${flow_id}`);
  const canvas = page.getByTestId('flow-canvas');
  await expect(page.locator('.svelte-flow__node')).toHaveCount(3);
  await expect(canvas.locator('.node')).toHaveCount(3);

  await page.getByTestId('canvas-view-bpmn').click();
  await expect(canvas.locator('.bpmn.start')).toHaveCount(1);
  await expect(canvas.locator('.bpmn.gateway')).toHaveCount(1);
  await expect(canvas.locator('.bpmn.end')).toHaveCount(1);

  await page.getByTestId('canvas-view-cards').click();
  await expect(canvas.locator('.node')).toHaveCount(3);
});

test('edits per-stage promotion policy from the deployment panel', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Promotion Policy' }
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
  await request.post(`/v1/flows/${flow_id}/deployments`, {
    headers: { 'X-Api-Key': KEY },
    data: { environment: 'sandbox', version: 1 }
  });

  await page.goto(`/engine/${flow_id}`);
  await page.getByTestId('tab-deploy').click();
  await expect(page.getByTestId('deploy-panel')).toContainText('sandbox: v1');
  await page.getByTestId('promotion-policy').locator('summary').click();
  await page.locator('.policy-stage', { hasText: 'staging' }).getByLabel('review request').check();
  await expect(page.getByText('Promotion policy saved')).toBeVisible();

  page.once('dialog', (d) => void d.accept()); // promotion confirms first
  await page.getByTestId('promote-submit').click();
  await expect(page.getByText(/Proposed v1 for staging/)).toBeVisible();
  await expect(page.getByTestId('deployment-requests')).toContainText('staging');
});

test('batch-decides a dataset from the builder', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Batched' }
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

  await page.goto(`/engine/${flow_id}`);
  await expect(page.locator('.svelte-flow__node')).toHaveCount(3); // version loaded
  await page.getByLabel('batch dataset').fill('[{}, {}, {}]');
  await page.getByTestId('run-batch').click();
  const summary = page.getByTestId('batch-summary');
  await expect(summary).toContainText('3 decided');
  await expect(summary).toContainText('3 completed');
  // Each row recorded a real decision (a link to its detail).
  await expect(page.getByRole('link', { name: 'view' }).first()).toBeVisible();
});

test('defines and runs flow assertions', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Asserted' }
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

  await page.goto(`/engine/${flow_id}`);
  await expect(page.locator('.svelte-flow__node')).toHaveCount(3);

  // One passing case (d == OK), one failing (d == NOPE).
  await page
    .getByLabel('assertion cases')
    .fill(
      '[{"name":"good","input":{},"expect":{"d":"OK"}},{"name":"bad","input":{},"expect":{"d":"NOPE"}}]'
    );
  await page.getByTestId('save-assertions').click();
  await expect(async () => {
    await page.getByTestId('run-assertions').click();
    await expect(page.getByTestId('assert-summary')).toContainText('1 passed');
  }).toPass();
  await expect(page.getByTestId('assert-summary')).toContainText('1 failed');
});

test('promotes an approved batch into pre-approvals', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Promoted' }
  });
  const { flow_id } = await created.json();
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
  // Bind a policy: score >= 0.8 -> approve, else refer.
  const pol = await request.post('/v1/policies', {
    headers: { 'X-Api-Key': KEY },
    data: { name: `stp-${slug}`, flow_slug: slug }
  });
  const { policy_id } = await pol.json();
  await request.post(`/v1/policies/${policy_id}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: { spec: { rules: [{ when: 'score >= 0.8', disposition: 'approve' }], default: 'refer' } }
  });

  await page.goto(`/engine/${flow_id}`);
  await expect(page.locator('.svelte-flow__node')).toHaveCount(3);

  await page
    .getByLabel('batch dataset')
    .fill('[{"applicant_id": "a1", "score": 0.95}, {"applicant_id": "a2", "score": 0.4}]');
  await page.getByLabel('pre-approve entity type').fill('applicant');
  await page.getByLabel('pre-approve entity key').fill('applicant_id');

  // Retry the promote until the policy projection resolves (1 grant, 1 skipped).
  const summary = page.getByTestId('preapprove-summary');
  await expect(async () => {
    await page.getByTestId('run-preapprove').click();
    await expect(summary).toContainText('1 granted');
  }).toPass();
  await expect(summary).toContainText('1 skipped');

  // The granted entity now appears active on the pre-approvals page.
  await page.goto('/preapprovals');
  await expect(page.locator('tr', { hasText: 'a1' }).first()).toContainText('active');
});

test('defines an outcome monitor and sees it fire', async ({ page, request }) => {
  page.on('dialog', (d) => d.accept()); // removing a webhook now asks to confirm
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Watched' }
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

  await page.goto(`/engine/${flow_id}`);
  await expect(page.locator('.svelte-flow__node')).toHaveCount(2);
  await page.getByTestId('tab-monitor').click(); // monitors/drift live behind their tab

  // The drift panel renders; with no baseline yet it prompts to capture one.
  const drift = page.getByTestId('drift-panel');
  await expect(drift).toContainText('No baseline captured');

  // Define a volume monitor: fire above 2 decisions.
  const panel = page.getByTestId('monitors-panel');
  await panel.getByLabel('monitor metric').selectOption('volume');
  await panel.getByLabel('monitor op').selectOption('gt');
  await panel.getByLabel('monitor threshold').fill('2');
  await panel.getByTestId('add-monitor').click();
  await expect(panel.locator('.mon-rule')).toContainText('volume');
  await expect(panel.locator('.mon-state')).toHaveText('ok'); // 0 > 2 is false

  // Run three decisions, then check & notify — the monitor fires (no webhooks, so
  // nothing is delivered; real delivery is covered by the Go e2e).
  for (let i = 0; i < 3; i++) {
    await request.post(`/v1/flows/${slug}/sandbox/decide`, {
      headers: { 'X-Api-Key': KEY },
      data: { data: {} }
    });
  }
  await expect(async () => {
    await panel.getByTestId('check-monitors').click();
    await expect(panel.locator('.mon-state')).toHaveText('firing');
  }).toPass();

  // Webhook CRUD lives in the same panel (tenant-wide delivery targets).
  await panel.getByText('Notification webhooks').click(); // open the <details>
  await panel.getByLabel('webhook url').fill('https://hooks.example.com/alerts');
  await panel.getByTestId('add-webhook').click();
  const hookRow = panel.locator('li', { hasText: 'hooks.example.com' });
  await expect(hookRow).toBeVisible();
  await hookRow.getByRole('button', { name: 'remove' }).click();
  await expect(panel.locator('li', { hasText: 'hooks.example.com' })).toHaveCount(0);
});

test('exports the flow as DOT and JSON', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Exported' }
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

  await page.goto(`/engine/${flow_id}`);
  // The published version loads (2 nodes on the canvas) before we export it.
  await expect(page.locator('.svelte-flow__node')).toHaveCount(2);
  await page.getByTestId('share-menu').locator('> summary').click(); // Export / Import

  for (const [name, ext] of [
    ['DOT', 'dot'],
    ['JSON', 'json']
  ] as const) {
    const [download] = await Promise.all([
      page.waitForEvent('download'),
      page.getByRole('button', { name, exact: true }).click()
    ]);
    expect(download.suggestedFilename()).toBe(`${slug}.${ext}`);
  }
});

test('imports a flow JSON onto the canvas and publishes it (round-trip)', async ({
  page,
  request
}) => {
  const H = { 'X-Api-Key': KEY };
  // Source flow A with a 3-node graph and an input schema.
  const slugA = uniqueSlug();
  const a = await request.post('/v1/flows', { headers: H, data: { slug: slugA, name: 'Source' } });
  const { flow_id: aId } = await a.json();
  await request.post(`/v1/flows/${aId}/versions`, {
    headers: H,
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
      },
      input_schema: { type: 'object', required: ['x'] }
    }
  });
  // Export A as JSON (the round-trippable form).
  const exported = await (
    await request.get(`/v1/flows/${aId}/export?format=json`, { headers: H })
  ).text();

  // Empty target flow B; import A's JSON into it via the builder.
  const slugB = uniqueSlug();
  const b = await request.post('/v1/flows', { headers: H, data: { slug: slugB, name: 'Target' } });
  const { flow_id: bId } = await b.json();

  await page.goto(`/engine/${bId}`);
  await openTools(page);
  await page.getByTestId('share-menu').locator('> summary').click(); // Export / Import
  await page.getByText('Import JSON', { exact: true }).click(); // open the disclosure
  await page.getByLabel('import flow json').fill(exported);
  await page.getByTestId('import-load').click();

  // The imported graph is on the canvas, then published as B's first version.
  await expect(page.locator('.svelte-flow__node')).toHaveCount(3);
  await page.getByRole('button', { name: 'Publish version' }).click();
  await expect(page.getByText(/Published v1/)).toBeVisible();

  // Round-trip integrity: B v1 carries the same graph and input schema.
  const got = await (await request.get(`/v1/flows/${bId}`, { headers: H })).json();
  const v = got.versions.at(-1);
  expect(v.graph.nodes).toHaveLength(3);
  expect(v.input_schema).toEqual({ type: 'object', required: ['x'] });
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
  await openTools(page);

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
  await page.getByRole('button', { name: 'Run', exact: true }).click();
  await expect(page.getByTestId('run-result')).toContainText('BUILT');
});

test('adding a node does not move already-placed nodes (stable layout)', async ({
  page,
  request
}) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Stable' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  await openTools(page);

  // Place input (n1) + output (n2), wire and publish v1.
  for (const type of ['input', 'output']) {
    await page.getByLabel('new node type').selectOption(type);
    await page.getByRole('button', { name: 'Add', exact: true }).click();
  }
  await expect(page.locator('aside ul.nodes li')).toHaveCount(2);
  await page.getByLabel('edge from', { exact: true }).selectOption('n1');
  await page.getByLabel('edge to', { exact: true }).selectOption('n2');
  await page.getByRole('button', { name: 'Add edge' }).click();
  await page.getByRole('button', { name: 'Publish version' }).click();
  await expect(page.getByText('Published v1')).toBeVisible();

  // Positions are persisted with the version.
  type Flow = {
    versions: { graph: { nodes: { id: string; position?: { x: number; y: number } }[] } }[];
  };
  const posIn = async (version: number) => {
    const f = (await (
      await request.get(`/v1/flows/${flow_id}`, {
        headers: { 'X-Api-Key': KEY }
      })
    ).json()) as Flow;
    const n1 = f.versions[version - 1].graph.nodes.find((n) => n.id === 'n1');
    return n1?.position;
  };
  const before = await posIn(1);
  expect(before, 'n1 position is persisted').toBeTruthy();

  // Add a third node, wire it in (the dry-compile rejects dangling nodes),
  // and republish — n1 must not have moved.
  await page.getByLabel('new node type').selectOption('assignment');
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await expect(page.locator('aside ul.nodes li')).toHaveCount(3);
  await page.getByLabel('edge from', { exact: true }).selectOption('n1');
  await page.getByLabel('edge to', { exact: true }).selectOption('n3');
  await page.getByRole('button', { name: 'Add edge' }).click();
  await page.getByLabel('edge from', { exact: true }).selectOption('n3');
  await page.getByLabel('edge to', { exact: true }).selectOption('n2');
  await page.getByRole('button', { name: 'Add edge' }).click();
  await page.getByRole('button', { name: 'Publish version' }).click();
  await expect(page.getByText('Published v2')).toBeVisible();

  const after = await posIn(2);
  expect(after).toEqual(before);
});

test('assigns nodes to swimlanes that render and persist', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Laned' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  await openTools(page);
  for (const type of ['input', 'output']) {
    await page.getByLabel('new node type').selectOption(type);
    await page.getByRole('button', { name: 'Add', exact: true }).click();
  }
  await expect(page.locator('aside ul.nodes li')).toHaveCount(2);

  // Put each node in its own lane via the side panel.
  await page.locator('aside ul.nodes button.link').filter({ hasText: 'n1' }).click();
  await page.getByLabel('node lane').fill('Intake');
  await page.locator('aside ul.nodes button.link').filter({ hasText: 'n2' }).click();
  await page.getByLabel('node lane').fill('Decision');

  // Two lanes → labelled backdrops render on the canvas.
  const canvas = page.getByTestId('flow-canvas');
  await expect(canvas).toContainText('Intake');
  await expect(canvas).toContainText('Decision');

  // Lanes persist with the published version (wired: the dry-compile rejects
  // a dangling input).
  await page.getByLabel('edge from', { exact: true }).selectOption('n1');
  await page.getByLabel('edge to', { exact: true }).selectOption('n2');
  await page.getByRole('button', { name: 'Add edge' }).click();
  await page.getByRole('button', { name: 'Publish version' }).click();
  await expect(page.getByText('Published v1')).toBeVisible();
  const flow = (await (
    await request.get(`/v1/flows/${flow_id}`, { headers: { 'X-Api-Key': KEY } })
  ).json()) as { versions: { graph: { nodes: { id: string; lane?: string }[] } }[] };
  const n1 = flow.versions[0].graph.nodes.find((n) => n.id === 'n1');
  expect(n1?.lane).toBe('Intake');
});

test('a structured config panel edits a node without raw JSON', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Structured' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  await openTools(page);

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
  await openTools(page);

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
  await openTools(page);

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

test('a decision-table panel sets a hit policy + aggregate without raw JSON', async ({
  page,
  request
}) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Table' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  await openTools(page);

  await page.getByLabel('new node type').selectOption('decision_table');
  await page.getByRole('button', { name: 'Add', exact: true }).click();

  // COLLECT reveals the aggregate picker; pick sum, then add a row + output.
  await page.getByLabel('decision table hit policy').selectOption('collect');
  await page.getByLabel('decision table aggregate').selectOption('sum');
  await page.getByRole('button', { name: 'Add row' }).click();
  await page.getByLabel('row 0 when').fill('score >= 80');
  await page.getByRole('button', { name: 'Add output' }).click();
  await page.getByLabel('row 0 output 0 target').fill('pts');
  await page.getByLabel('row 0 output 0 expr').fill('2');

  await expect(page.getByLabel('node config')).toHaveValue(
    '{"hit":"collect","aggregate":"sum","rows":[{"when":"score >= 80","outputs":[{"target":"pts","expr":"2"}]}]}'
  );
});

test('the authoring copilot explains a flow and suggests logic', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Copilot' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  await openTools(page);
  await page.getByTestId('tab-copilot').click();

  // Suggest from a description (the dev server's Stub provider echoes deterministically).
  await page.getByLabel('copilot prompt').fill('approve when fico >= 720');
  await page.getByRole('button', { name: 'Suggest logic' }).click();
  await expect(page.getByTestId('copilot-output')).toBeVisible();
  await expect(page.getByTestId('copilot-output')).toContainText('fico >= 720');

  // Explain the (empty) flow.
  await page.getByRole('button', { name: 'Explain this flow' }).click();
  await expect(page.getByTestId('copilot-output')).toBeVisible();

  // Generate validates server-side; the Stub can't produce a valid flow, so the
  // 422 surfaces gracefully (nothing is applied) rather than crashing.
  await page.getByRole('button', { name: 'Generate & apply a flow' }).click();
  await expect(page.getByText(/not valid|did not return a usable graph/)).toBeVisible();
});

test('a rule panel edits when/then clauses without raw JSON', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Rule' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  await openTools(page);

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

test('a reason panel edits adverse-action codes without raw JSON', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Reason' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  await openTools(page);

  await page.getByLabel('new node type').selectOption('reason');
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await page.getByRole('button', { name: 'Add reason' }).click();
  await page.getByLabel('reason 0 when').fill('fico < 600');
  await page.getByLabel('reason 0 code').fill('R01');
  await page.getByLabel('reason 0 description').fill('Insufficient credit score');

  await expect(page.getByLabel('node config')).toHaveValue(
    '{"reasons":[{"when":"fico < 600","code":"R01","description":"Insufficient credit score"}]}'
  );
});

test('backtests a dataset and diffs two versions', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Backtested' }
  });
  const { flow_id } = await created.json();

  // v1 always decides A; v2 decides A or B depending on the score.
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
  for (const expr of ["'A'", 'score > 5 ? "A" : "B"']) {
    const pub = await request.post(`/v1/flows/${flow_id}/versions`, {
      headers: { 'X-Api-Key': KEY },
      data: { graph: constGraph(expr) }
    });
    expect(pub.ok()).toBeTruthy();
  }

  await page.goto(`/engine/${flow_id}`);
  await expect(page.locator('.svelte-flow__node')).toHaveCount(3);

  // Backtest the latest version (v2) against v1 over a two-row dataset: one row
  // keeps the same outcome, the other flips A -> B.
  await page.getByLabel('compare version').fill('1');
  await page.getByLabel('backtest dataset').fill('[{"score": 10}, {"score": 1}]');
  await page.getByTestId('run-backtest').click();

  const summary = page.getByTestId('backtest-summary');
  await expect(summary).toContainText('2 records');
  await expect(summary).toContainText('1 changed');
  // The changed row shows the candidate flipping to B.
  await expect(page.locator('.bt-table tbody tr')).toHaveCount(1);
  await expect(page.locator('.bt-table tbody tr')).toContainText('B');
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
  await openTools(page);
  await page.getByLabel('new node type').selectOption('rule');
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await page.getByRole('button', { name: 'Publish version' }).click();
  // A write failure now surfaces as a toast beside the action, not the old banner.
  await expect(page.locator('.toast.error')).toContainText('input');
});

// --- Design window (Miro-style board) ---------------------------------------------

test('clicking a canvas node opens its inspector; pane click closes it', async ({
  page,
  request
}) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Inspect' }
  });
  const { flow_id } = await created.json();
  await request.post(`/v1/flows/${flow_id}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input' },
          { id: 'gate', type: 'split', config: { condition: 'x > 1' } },
          { id: 'out', type: 'output' }
        ],
        edges: [
          { from: 'in', to: 'gate' },
          { from: 'gate', to: 'out', branch: 'yes' },
          { from: 'gate', to: 'out', branch: 'no' }
        ]
      }
    }
  });

  await page.goto(`/engine/${flow_id}`);
  await expect(page.locator('.svelte-flow__node')).toHaveCount(3);
  await page.locator('.svelte-flow__node', { hasText: 'gate' }).click();

  const inspector = page.getByTestId('node-inspector');
  await expect(inspector).toBeVisible();
  await expect(inspector.getByLabel('condition')).toHaveValue('x > 1');

  // Edit through the inspector — the config JSON follows.
  await inspector.getByLabel('condition').fill('x > 5');
  await expect(inspector.getByLabel('node config')).toHaveValue(/x > 5/);

  // Clicking empty canvas deselects and closes the inspector.
  await page.locator('.svelte-flow__pane').click({ position: { x: 620, y: 420 } });
  await expect(inspector).toHaveCount(0);
});

test('the node rail inserts a node and opens its inspector', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Rail' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  await expect(page.getByTestId('node-rail')).toBeVisible();
  await page.getByRole('button', { name: 'insert split node' }).click();

  const inspector = page.getByTestId('node-inspector');
  await expect(inspector).toBeVisible();
  await expect(inspector.getByLabel('selected node type')).toHaveValue('split');
});

test('the design window focuses, exits on Escape, and collapses', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Modes' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  const canvas = page.getByTestId('flow-canvas');

  await page.getByTestId('canvas-mode-focus').click();
  await expect(canvas).toHaveClass(/focus/);
  // Focus takes the whole viewport.
  const box = await canvas.boundingBox();
  expect(box?.width).toBe(page.viewportSize()?.width);

  await page.keyboard.press('Escape');
  await expect(canvas).not.toHaveClass(/focus/);

  await page.getByTestId('canvas-mode-collapsed').click();
  await expect(canvas).toHaveClass(/collapsed/);
  await expect(page.locator('.svelte-flow')).toHaveCount(0);
  // The collapsed strip names the graph size and expands back to the board.
  await page.getByTestId('canvas-mode-board').click();
  await expect(page.locator('.svelte-flow')).toBeVisible();
});

test('clicking an edge opens its inspector; the branch label edits live', async ({
  page,
  request
}) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Edges' }
  });
  const { flow_id } = await created.json();
  await request.post(`/v1/flows/${flow_id}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input' },
          { id: 'gate', type: 'split', config: { condition: 'x > 1' } },
          { id: 'out', type: 'output' },
          // The no branch lands on its own node so the two gate edges don't overlap
          // (a click on stacked identical curves would hit the wrong one).
          { id: 'decline', type: 'output' }
        ],
        edges: [
          { from: 'in', to: 'gate' },
          { from: 'gate', to: 'out', branch: 'yes' },
          { from: 'gate', to: 'decline', branch: 'no' }
        ]
      }
    }
  });

  await page.goto(`/engine/${flow_id}`);
  await expect(page.locator('.svelte-flow__node')).toHaveCount(4);
  // Click a point ON a branch edge's interaction path — a curve's bounding-box
  // center (what a plain click targets) usually misses the path itself. The split's
  // two branch edges are the last two; either exercises the inspector.
  const pt = await page
    .locator('.svelte-flow__edge-interaction')
    .last()
    .evaluate((el) => {
      const path = el as SVGPathElement;
      const mid = path.getPointAtLength(path.getTotalLength() / 2);
      const ctm = path.getScreenCTM();
      if (!ctm) throw new Error('edge path has no CTM');
      const sp = new DOMPoint(mid.x, mid.y).matrixTransform(ctm);
      return { x: sp.x, y: sp.y };
    });
  await page.mouse.click(pt.x, pt.y);

  const inspector = page.getByTestId('edge-inspector');
  await expect(inspector).toBeVisible();
  // The clicked edge carries a branch (yes or no — ordering isn't guaranteed after
  // the backend's layout pass); editing it updates the on-canvas label live.
  const branchInput = inspector.getByLabel('edge branch label');
  expect(['yes', 'no']).toContain(await branchInput.inputValue());
  await branchInput.fill('approved');
  await expect(page.locator('.svelte-flow__edge-label').getByText('approved')).toBeVisible();

  await inspector.getByRole('button', { name: 'Delete edge' }).click();
  await expect(inspector).toHaveCount(0);
  // Deleting one branch edge leaves in→gate and the other branch.
  await expect(page.locator('.svelte-flow__edge')).toHaveCount(2);
});

test('dragging a rail type onto the board places the node at the drop point', async ({
  page,
  request
}) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'DnD' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  const pane = page.locator('.svelte-flow__pane');
  await expect(pane).toBeVisible();
  const pb = await pane.boundingBox();
  if (!pb) throw new Error('pane has no box');
  await page.getByRole('button', { name: 'insert split node' }).dragTo(pane, {
    targetPosition: { x: Math.round(pb.width * 0.6), y: Math.round(pb.height * 0.6) }
  });

  const inspector = page.getByTestId('node-inspector');
  await expect(inspector).toBeVisible();
  await expect(inspector.getByLabel('selected node type')).toHaveValue('split');

  // Duplicate stamps a configured copy and moves selection to it (a fresh
  // unpublished flow has no other canvas nodes: dropped split + its copy = 2).
  await inspector.getByLabel('condition').fill('x > 9');
  await inspector.getByRole('button', { name: 'Duplicate' }).click();
  await expect(inspector.getByLabel('condition')).toHaveValue('x > 9');
  await expect(page.locator('.svelte-flow__node-flow')).toHaveCount(2);
});

test('keyboard toggles: f focuses the board, t opens the tools panel', async ({
  page,
  request
}) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Keys' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  const canvas = page.getByTestId('flow-canvas');
  await expect(canvas).toBeVisible();

  await page.keyboard.press('f');
  await expect(canvas).toHaveClass(/focus/);
  await page.keyboard.press('Escape');
  await expect(canvas).not.toHaveClass(/focus/);

  await page.keyboard.press('t');
  await expect(page.getByLabel('new node type')).toBeVisible();
  // Typing "t" in a field must NOT toggle the panel.
  await page.getByLabel('new node type').focus();
  await page.keyboard.press('t');
  await expect(page.getByLabel('new node type')).toBeVisible();

  // The tools-panel choice persists across a reload.
  await page.reload();
  await expect(page.getByLabel('new node type')).toBeVisible();
});

test('a brand-new flow opens to a guided blank board, error-free', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Blank' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  await expect(page.getByTestId('board-empty')).toBeVisible();
  // The unpublished flow's omitted `versions` must not surface as an error
  // ("versions is not iterable" regression).
  await expect(page.locator('p.err')).toHaveCount(0);
});

test('select and pan tools: marquee selects, pan does not, v/h switch', async ({
  page,
  request
}) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Tools' }
  });
  const { flow_id } = await created.json();
  await request.post(`/v1/flows/${flow_id}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input', position: { x: 0, y: 0 } },
          { id: 'a', type: 'assignment', position: { x: 240, y: 0 } },
          { id: 'out', type: 'output', position: { x: 480, y: 0 } }
        ],
        edges: [
          { from: 'in', to: 'a' },
          { from: 'a', to: 'out' }
        ]
      }
    }
  });

  await page.goto(`/engine/${flow_id}`);
  await expect(page.locator('.svelte-flow__node')).toHaveCount(3);

  // Select (default): dragging over nodes marquee-selects them.
  const first = await page.locator('.svelte-flow__node').first().boundingBox();
  const last = await page.locator('.svelte-flow__node').last().boundingBox();
  if (!first || !last) throw new Error('nodes have no boxes');
  await page.mouse.move(first.x - 20, first.y - 20);
  await page.mouse.down();
  await page.mouse.move(last.x + last.width + 20, last.y + last.height + 20, { steps: 6 });
  await page.mouse.up();
  await expect(page.locator('.svelte-flow__node.selected')).toHaveCount(3);

  // h → pan tool: the same drag selects nothing (it pans the board). A plain
  // click on empty canvas clears the marquee selection first.
  await page.mouse.click(first.x - 40, first.y + 200);
  await expect(page.locator('.svelte-flow__node.selected')).toHaveCount(0);
  await page.keyboard.press('h');
  await expect(page.getByTestId('tool-pan')).toHaveAttribute('aria-pressed', 'true');
  await page.mouse.move(first.x - 20, first.y - 20);
  await page.mouse.down();
  await page.mouse.move(first.x + 60, first.y + 60, { steps: 4 });
  await page.mouse.up();
  await expect(page.locator('.svelte-flow__node.selected')).toHaveCount(0);

  // v → back to select.
  await page.keyboard.press('v');
  await expect(page.getByTestId('tool-select')).toHaveAttribute('aria-pressed', 'true');

  // Zoom controls live at the bottom-right, clear of the node rail.
  const controls = await page.locator('.svelte-flow__controls').boundingBox();
  const rail = await page.getByTestId('node-rail').boundingBox();
  const pane = await page.locator('.svelte-flow__pane').boundingBox();
  if (!controls || !rail || !pane) throw new Error('missing boxes');
  expect(controls.x).toBeGreaterThan(pane.x + pane.width / 2);
  // No overlap between the rail and the controls.
  expect(rail.x + rail.width).toBeLessThan(controls.x);
});

test('creates a flow with a description, shows it in list + builder, and edits it inline', async ({
  page
}) => {
  const slug = uniqueSlug();
  await page.goto('/engine');
  await page.getByLabel('slug').fill(slug);
  await page.getByLabel('name').fill('Described Flow');
  await page.getByLabel('description').fill('Scores small-business loans nightly.');
  await page.getByRole('button', { name: 'Create flow' }).click();

  // Creation lands in the builder, where the description shows under the title.
  await expect(page).toHaveURL(/\/engine\/.+/);
  const desc = page.getByTestId('flow-description');
  await expect(desc).toContainText('Scores small-business loans nightly.');

  // The list shows the description as a muted line under the name.
  await page.goto('/engine');
  const row = page.locator('tbody tr').filter({ hasText: slug });
  await expect(row).toContainText('Scores small-business loans nightly.');

  // Inline edit: pencil → textarea → save; the change survives a reload.
  await row.getByRole('link').click();
  await page.getByRole('button', { name: 'Edit description' }).click();
  await page.getByLabel('flow description').fill('Scores loans and flags fraud.');
  await page.getByRole('button', { name: 'Save description' }).click();
  await expect(page.getByTestId('flow-description')).toContainText('Scores loans and flags fraud.');
  await page.reload();
  await expect(page.getByTestId('flow-description')).toContainText('Scores loans and flags fraud.');
});

test('the header robot button analyzes the flow with AI inline', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Analyzed' }
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

  await page.goto(`/engine/${flow_id}`);
  await expect(page.locator('.svelte-flow__node')).toHaveCount(2);
  await page.getByTestId('analyze-flow').click();

  // The analysis surfaces in a panel by the header (the AI stub returns
  // deterministic text — assert something non-empty arrived, not exact wording).
  await expect(page.getByTestId('analyze-panel')).toBeVisible();
  const out = page.getByTestId('analyze-output');
  await expect(out).toBeVisible();
  await expect(out).toContainText(/\S/);
  await page.getByRole('button', { name: 'Close analysis' }).click();
  await expect(page.getByTestId('analyze-panel')).toHaveCount(0);
});

test('sample dataset and sweep buttons prefill runnable inputs', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Samples' }
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
            config: { assignments: [{ target: 'ok', expr: 'score > 600' }] },
            position: { x: 200, y: 0 }
          },
          { id: 'out', type: 'output', position: { x: 400, y: 0 } }
        ],
        edges: [
          { from: 'in', to: 'a' },
          { from: 'a', to: 'out' }
        ]
      },
      input_schema: {
        type: 'object',
        properties: {
          score: { type: 'integer', example: 680 },
          segment: { type: 'string', enum: ['retail', 'smb'] }
        }
      }
    }
  });

  await page.goto(`/engine/${flow_id}`);
  await page.getByTestId('tab-test').click();

  // Backtest: 8 varied rows; integers stay whole; enums cycle.
  await page.getByTestId('sample-backtest').click();
  const rows = JSON.parse(await page.getByLabel('backtest dataset').inputValue());
  expect(rows).toHaveLength(8);
  expect(new Set(rows.map((r: { score: number }) => r.score)).size).toBeGreaterThan(3);
  for (const r of rows) expect(Number.isInteger(r.score)).toBe(true);
  expect(new Set(rows.map((r: { segment: string }) => r.segment))).toEqual(
    new Set(['retail', 'smb'])
  );

  // What-if: prefills the numeric field, a 5-value sweep, and a base row — and runs.
  await page.getByTestId('sample-whatif').click();
  await expect(page.getByLabel('whatif field')).toHaveValue('score');
  expect((await page.getByLabel('whatif values').inputValue()).split(',')).toHaveLength(5);
  await page.getByTestId('run-whatif').click();
  await expect(page.getByTestId('whatif-summary')).toBeVisible();

  // Batch: same dataset shape.
  await page.getByTestId('sample-batch').click();
  expect(JSON.parse(await page.getByLabel('batch dataset').inputValue())).toHaveLength(8);
});
