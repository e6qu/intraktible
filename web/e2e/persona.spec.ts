// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test('a persona composes its own navigation and landing', async ({ page, context }) => {
  await context.request.post('/v1/login', { data: { api_key: KEY } });

  // Developer / Integrator: Decisions is relabelled "Traces", Cases is not in this
  // persona's nav, and the config-driven persona home renders.
  await page.goto('/');
  await page.evaluate(() => localStorage.setItem('intraktible-persona', 'developer'));
  await page.reload();
  const primary = page.getByRole('navigation', { name: 'Primary' });
  await expect(primary.getByText('Traces')).toBeVisible();
  await expect(primary.getByText('Cases')).toHaveCount(0);
  const home = page.getByTestId('persona-home');
  await expect(home).toBeVisible();
  await expect(home).toContainText('Developer / Integrator');

  // Workflow Designer composes a different navigation (Engine present, Decisions not
  // relabelled) and lands on its bespoke deck, not the generic persona home.
  await page.evaluate(() => localStorage.setItem('intraktible-persona', 'builder'));
  await page.reload();
  const designerNav = page.getByRole('navigation', { name: 'Primary' });
  await expect(designerNav.getByText('Engine')).toBeVisible();
  await expect(designerNav.getByText('Traces')).toHaveCount(0);
  await expect(page.getByTestId('persona-home')).toHaveCount(0);
});
