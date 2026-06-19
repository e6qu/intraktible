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
  await expect(page.getByRole('heading', { name: slug })).toBeVisible();
  await expect(page.locator('ol.trace')).toContainText('assignment');
  await expect(page.getByRole('button', { name: 'Sequence', exact: true })).toBeVisible();

  // The run trace exports as DOT and JSON (the decision record).
  for (const [name, file] of [
    ['DOT', `${decisionId}-trace.dot`],
    ['JSON', `${decisionId}.json`]
  ] as const) {
    const [dl] = await Promise.all([
      page.waitForEvent('download'),
      page.getByRole('button', { name, exact: true }).click()
    ]);
    expect(dl.suggestedFilename()).toBe(file);
  }

  // The decision id is click-to-copy (a developer DX affordance).
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.getByTestId('copyable').click();
  await expect(page.getByText('Copied decision id')).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(decisionId);
});

test('the decision trace surfaces the split branch it routed through', async ({
  page,
  request
}) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Branch Flow' }
  });
  const { flow_id } = await created.json();
  await request.post(`/v1/flows/${flow_id}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input' },
          { id: 'gate', type: 'split', config: { condition: 'score >= 700' } },
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

  let decisionId = '';
  await expect(async () => {
    const r = await request.post(`/v1/flows/${slug}/production/decide`, {
      headers: { 'X-Api-Key': KEY },
      data: { data: { score: 800 } }
    });
    const body = await r.json();
    expect(body.status).toBe('completed');
    decisionId = body.decision_id;
  }).toPass({ timeout: 5000 });

  // The trace shows which way the split routed (score 800 ≥ 700 → "yes").
  await page.goto(`/decisions/${decisionId}`);
  await expect(page.getByTestId('trace-branch')).toContainText('yes');
});

test('a bound policy assigns a disposition shown on the decision detail', async ({
  page,
  request
}) => {
  const H = { 'X-Api-Key': KEY };
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', { headers: H, data: { slug, name: 'Scored' } });
  const { flow_id } = await created.json();
  await request.post(`/v1/flows/${flow_id}/versions`, {
    headers: H,
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
  // A policy bound to the flow: high score auto-approves.
  const pol = await request.post('/v1/policies', {
    headers: H,
    data: { name: 'scored-stp', flow_slug: slug }
  });
  const { policy_id } = await pol.json();
  await request.post(`/v1/policies/${policy_id}/versions`, {
    headers: H,
    data: {
      spec: {
        rules: [{ when: 'score >= 0.85', disposition: 'approve', code: 'P-AUTO' }],
        default: 'refer'
      }
    }
  });

  // Decide a high score; retry until both the flow and policy projections are live.
  let decisionId = '';
  await expect(async () => {
    const r = await request.post(`/v1/flows/${slug}/production/decide`, {
      headers: H,
      data: { data: { score: 0.9 } }
    });
    const body = await r.json();
    expect(body.disposition).toBe('approve');
    decisionId = body.decision_id;
  }).toPass({ timeout: 5000 });

  await page.goto(`/decisions/${decisionId}`);
  await expect(page.locator('.disp.approve')).toHaveText('approve');
});

test('a reason node yields adverse-action reason codes on the decision detail', async ({
  page,
  request
}) => {
  const slug = uniqueSlug();
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: 'Adverse Flow' }
  });
  const { flow_id } = await created.json();
  await request.post(`/v1/flows/${flow_id}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input' },
          {
            id: 'r',
            type: 'reason',
            config: {
              reasons: [
                { when: 'fico < 600', code: 'R01', description: 'Insufficient credit score' },
                { when: 'income < 30000', code: 'R02', description: 'Insufficient income' }
              ]
            }
          },
          { id: 'out', type: 'output', config: { fields: ['decision'] } }
        ],
        edges: [
          { from: 'in', to: 'r' },
          { from: 'r', to: 'out' }
        ]
      }
    }
  });

  let decisionId = '';
  await expect(async () => {
    const r = await request.post(`/v1/flows/${slug}/production/decide`, {
      headers: { 'X-Api-Key': KEY },
      data: { data: { fico: 500, income: 50000 } }
    });
    const body = await r.json();
    expect(body.status).toBe('completed');
    decisionId = body.decision_id;
  }).toPass({ timeout: 5000 });

  // Only fico<600 matched → exactly one reason code, surfaced first-class.
  await page.goto(`/decisions/${decisionId}`);
  const reasons = page.getByTestId('reason-codes');
  await expect(reasons).toBeVisible();
  await expect(reasons.locator('li')).toHaveCount(1);
  await expect(reasons).toContainText('R01');
  await expect(reasons).toContainText('Insufficient credit score');
});
