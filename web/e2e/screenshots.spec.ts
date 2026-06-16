// SPDX-License-Identifier: AGPL-3.0-or-later
// Design review helper: seeds a little data, then captures every route in light
// and dark mode to /tmp/itk-shots for visual review. Not a correctness test.
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';
const SHOTS = process.env.ITK_SHOTS_DIR ?? '/tmp/itk-shots';

// Opt-in: this is a design-review helper, not a correctness test, so it stays out
// of the normal suite (and CI). Run it with: ITK_SHOTS=1 npx playwright test screenshots
test.skip(!process.env.ITK_SHOTS, 'set ITK_SHOTS=1 to capture design-review screenshots');

// Run on a single worker so beforeAll seeds the (slug-unique) fixtures exactly
// once — parallel workers would collide on the flow slugs and leave ids unset.
test.describe.configure({ mode: 'serial' });

// The personas re-skin and re-prioritise the whole app; capture each so the
// design review can compare the three viewer experiences side by side.
const PERSONAS = ['builder', 'operator', 'showcase'];

let flowId = '';
let caseId = '';
let decisionId = '';
let entityType = '';
let entityId = '';

test.beforeAll(async ({ request }) => {
  const flow = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug: 'shot-credit', name: 'Credit Decision' }
  });
  flowId = (await flow.json()).flow_id;
  await request.post(`/v1/flows/${flowId}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input' },
          { id: 's', type: 'split', name: 'score check' },
          { id: 'ai', type: 'ai', name: 'assess' },
          { id: 'mr', type: 'manual_review', name: 'review' },
          { id: 'out', type: 'output' }
        ],
        edges: [
          { from: 'in', to: 's' },
          { from: 's', to: 'ai', branch: 'yes' },
          { from: 's', to: 'mr', branch: 'no' },
          { from: 'ai', to: 'out' },
          { from: 'mr', to: 'out' }
        ]
      }
    }
  });
  const c = await request.post('/v1/cases', {
    headers: { 'X-Api-Key': KEY },
    data: { company_name: 'Acme Corp', case_type: 'aml', sla_days: 5 }
  });
  caseId = (await c.json()).case_id;
  await request.post('/v1/agents', {
    headers: { 'X-Api-Key': KEY },
    data: { name: 'screener', system: 'screen applicants', tools: ['bureau'] }
  });
  await request.post('/v1/agents/screener/run', {
    headers: { 'X-Api-Key': KEY },
    data: { prompt: 'screen this applicant' }
  });
  // A simple decideable flow + a run so /decisions has content.
  const simple = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug: 'shot-simple', name: 'Approval' }
  });
  const simpleId = (await simple.json()).flow_id;
  await request.post(`/v1/flows/${simpleId}/versions`, {
    headers: { 'X-Api-Key': KEY },
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input' },
          {
            id: 'a',
            type: 'assignment',
            config: { assignments: [{ target: 'decision', expr: "'APPROVE'" }] }
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
  await expect(async () => {
    const r = await request.post('/v1/flows/shot-simple/production/decide', {
      headers: { 'X-Api-Key': KEY },
      data: { data: {} }
    });
    const body = await r.json();
    expect(body.status).toBe('completed');
    decisionId = body.decision_id;
  }).toPass({ timeout: 5000 });

  // Context Layer: a connector, a feature, and an entity with events so /data and
  // a /data detail page have real content.
  entityType = 'applicant';
  entityId = 'acme-42';
  await request.post('/v1/context/connectors', {
    headers: { 'X-Api-Key': KEY },
    data: {
      name: 'bureau',
      type: 'http',
      config: { url: 'https://bureau.example/score', api_key: 's3cr3t' }
    }
  });
  await request.post('/v1/context/features', {
    headers: { 'X-Api-Key': KEY },
    data: {
      name: 'txn_count_24h',
      entity_type: entityType,
      event_name: 'transaction',
      aggregation: 'count',
      window_hours: 24
    }
  });
  await request.post('/v1/context/entities', {
    headers: { 'X-Api-Key': KEY },
    data: {
      entity_type: entityType,
      entity_id: entityId,
      attributes: { name: 'Acme Corp', tier: 'gold', region: 'EU' }
    }
  });
  for (const amount of [120, 340, 75]) {
    await request.post('/v1/context/events', {
      headers: { 'X-Api-Key': KEY },
      data: {
        entity_type: entityType,
        entity_id: entityId,
        event_name: 'transaction',
        data: { amount }
      }
    });
  }
});

test('screenshots — mobile', async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
  await page.setViewportSize({ width: 390, height: 844 });
  for (const [label, path] of Object.entries({
    home: '/',
    cases: '/cases',
    builder: `/engine/${flowId}`
  })) {
    await page.goto(path);
    await page.waitForTimeout(600);
    await page.screenshot({ path: `${SHOTS}/m-${label}.png`, fullPage: true });
  }
});

for (const themeMode of ['light', 'dark']) {
  test(`screenshots — ${themeMode}`, async ({ page }) => {
    await page.context().request.post('/v1/login', { data: { api_key: KEY } });
    await page.addInitScript((t) => localStorage.setItem('intraktible-theme', t), themeMode);
    const routes: Record<string, string> = {
      home: '/',
      login: '/login',
      engine: '/engine',
      builder: `/engine/${flowId}`,
      decisions: '/decisions',
      'decision-detail': `/decisions/${decisionId}`,
      cases: '/cases',
      'case-detail': `/cases/${caseId}`,
      agents: '/agents',
      'agent-detail': '/agents/screener',
      data: '/data',
      'data-detail': `/data/${entityType}/${entityId}`,
      audit: '/audit'
    };
    for (const [label, path] of Object.entries(routes)) {
      await page.goto(path);
      await page.waitForTimeout(700); // let async projections + canvas settle
      await page.screenshot({ path: `${SHOTS}/${label}-${themeMode}.png`, fullPage: true });
    }
  });
}

// Per-persona capture: the landing dashboard differs most by persona, plus a few
// representative pages to show the re-skin (accent / type / density).
for (const p of PERSONAS) {
  for (const themeMode of ['light', 'dark']) {
    test(`persona ${p} — ${themeMode}`, async ({ page }) => {
      await page.context().request.post('/v1/login', { data: { api_key: KEY } });
      await page.addInitScript(
        ([persona, t]) => {
          localStorage.setItem('intraktible-persona', persona);
          localStorage.setItem('intraktible-theme', t);
        },
        [p, themeMode]
      );
      const routes: Record<string, string> = {
        home: '/',
        engine: '/engine',
        cases: '/cases',
        audit: '/audit'
      };
      for (const [label, path] of Object.entries(routes)) {
        await page.goto(path);
        await page.waitForTimeout(900); // let count-ups + reveals settle
        await page.screenshot({
          path: `${SHOTS}/persona-${p}-${label}-${themeMode}.png`,
          fullPage: true
        });
      }
    });
  }
}
