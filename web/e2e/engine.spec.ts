// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';
const uniqueSlug = () => 'ui-' + Math.random().toString(36).slice(2, 9);

// The UI authenticates via the session cookie; sign the page context in first.
test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

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
            config: { branches: [{ name: 'yes', when: 'true' }] }
          },
          { id: 'out', type: 'output', name: 'Finish' }
        ],
        edges: [
          { from: 'in', to: 'gate' },
          { from: 'gate', to: 'out', branch: 'yes' }
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
  await expect(page.getByTestId('deploy-panel')).toContainText('sandbox: v1');
  await page.getByTestId('promotion-policy').locator('summary').click();
  await page.locator('.policy-stage', { hasText: 'staging' }).getByLabel('review request').check();
  await expect(page.getByText('Promotion policy saved')).toBeVisible();

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
    await request.post(`/v1/flows/${slug}/production/decide`, {
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
  await expect(page.getByLabel('new node type')).toBeVisible(); // hydrated
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
  await expect(page.getByLabel('new node type')).toBeVisible();

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

  // Add a third node and republish — n1 must not have moved.
  await page.getByLabel('new node type').selectOption('assignment');
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await expect(page.locator('aside ul.nodes li')).toHaveCount(3);
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
  await expect(page.getByLabel('new node type')).toBeVisible();
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

  // Lanes persist with the published version.
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

test('a reason panel edits adverse-action codes without raw JSON', async ({ page, request }) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Reason' }
  });
  const { flow_id } = await created.json();

  await page.goto(`/engine/${flow_id}`);
  await expect(page.getByLabel('new node type')).toBeVisible();

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
  await page.getByLabel('new node type').selectOption('rule');
  await page.getByRole('button', { name: 'Add', exact: true }).click();
  await page.getByRole('button', { name: 'Publish version' }).click();
  await expect(page.locator('.err')).toContainText('input');
});
