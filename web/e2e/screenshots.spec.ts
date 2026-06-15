// SPDX-License-Identifier: AGPL-3.0-or-later
// Design review helper: seeds a little data, then captures every route in light
// and dark mode to /tmp/itk-shots for visual review. Not a correctness test.
import { test } from '@playwright/test';

const KEY = 'dev-sandbox-key';
const SHOTS = process.env.ITK_SHOTS_DIR ?? '/tmp/itk-shots';

// Opt-in: this is a design-review helper, not a correctness test, so it stays out
// of the normal suite (and CI). Run it with: ITK_SHOTS=1 npx playwright test screenshots
test.skip(!process.env.ITK_SHOTS, 'set ITK_SHOTS=1 to capture design-review screenshots');

let flowId = '';

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
  await request.post('/v1/cases', {
    headers: { 'X-Api-Key': KEY },
    data: { company_name: 'Acme Corp', case_type: 'aml', sla_days: 5 }
  });
  await request.post('/v1/agents', {
    headers: { 'X-Api-Key': KEY },
    data: { name: 'screener', system: 'screen applicants', tools: ['bureau'] }
  });
});

for (const themeMode of ['light', 'dark']) {
  test(`screenshots — ${themeMode}`, async ({ page }) => {
    await page.addInitScript((t) => localStorage.setItem('intraktible-theme', t), themeMode);
    const routes: Record<string, string> = {
      home: '/',
      login: '/login',
      engine: '/engine',
      builder: `/engine/${flowId}`,
      cases: '/cases',
      agents: '/agents'
    };
    for (const [label, path] of Object.entries(routes)) {
      await page.goto(path);
      await page.waitForTimeout(700); // let async projections + canvas settle
      await page.screenshot({ path: `${SHOTS}/${label}-${themeMode}.png`, fullPage: true });
    }
  });
}
