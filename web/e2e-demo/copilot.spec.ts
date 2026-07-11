// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

// The public demo boots the REAL Go backend as wasm, with the AI provider
// simulated in TypeScript (src/lib/backend/ai-sim.ts). The flagship "Draft a flow
// with AI" button posts to /v1/copilot/generate, which asks the provider for a
// structured {nodes, edges} graph and rejects it server-side (ValidateFlow) unless
// it is a real, publishable graph. This is the roundtrip that a string-only sim
// broke (a 422 "did not return a usable graph"): it must now draft a real flow.
test('the AI flow copilot drafts a real, valid flow on the wasm demo', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));

  await page.goto('engine');
  // The copilot panel is open by default on the flow list.
  await expect(page.getByTestId('copilot-panel')).toBeVisible();
  await page
    .getByLabel('describe the flow')
    .fill('Screen a payment for fraud using transaction velocity and a new-device signal.');
  await page.getByRole('button', { name: 'Generate flow' }).click();

  // A real graph was drafted (not a 422): the draft summary lists its nodes, and
  // no error banner surfaced.
  const draft = page.getByTestId('copilot-draft');
  await expect(draft).toBeVisible();
  await expect(draft).toContainText(/Drafted flow — \d+ nodes/);
  await expect(page.locator('p.err')).toHaveCount(0);

  // The draft is applyable: opening it in the builder imports the graph (a second
  // server-side validation on publish) and lands on the flow's builder route.
  await page.getByTestId('open-draft').click();
  await expect(page).toHaveURL(/\/engine\/[^/]+$/);
  await expect(page.locator('h1, h2').first()).toBeVisible();

  expect(errors, `uncaught error(s): ${errors.join('; ')}`).toEqual([]);
});
