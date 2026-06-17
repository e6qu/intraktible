// SPDX-License-Identifier: AGPL-3.0-or-later
// Smoke tests against the SINGLE BINARY (`intraktible serve` with the real UI
// embedded), not the Vite dev/preview server the main suite uses. This is the
// artifact that actually ships; it is the only place a broken //go:embed (which
// once shipped a blank page) or a mis-mounted UI handler would surface.
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test('the embedded binary serves bootable assets, not the HTML shell (HTTP)', async ({
  request
}) => {
  const index = await request.get('/');
  expect(index.ok()).toBeTruthy();
  const html = await index.text();

  // SvelteKit emits JS/CSS under /_app; a real bundle must be referenced and must
  // serve as JavaScript. The original bug served index.html (text/html) for these,
  // so the app never booted.
  const js = html.match(/\/_app\/[^"']+\.js/);
  expect(js, 'index.html should reference an /_app JS bundle').toBeTruthy();
  if (!js) return;
  const asset = await request.get(js[0]);
  expect(asset.ok()).toBeTruthy();
  expect(asset.headers()['content-type'] ?? '').toContain('javascript');

  const css = html.match(/\/_app\/[^"']+\.css/);
  if (css) {
    const sheet = await request.get(css[0]);
    expect(sheet.headers()['content-type'] ?? '').toContain('css');
  }

  expect((await request.get('/healthz')).ok()).toBeTruthy();
});

test('the embedded UI boots in a browser and logs in', async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });

  // The header (rendered by the SPA) only appears if the embedded JS executed —
  // i.e. the assets loaded from the binary. A blank page fails here.
  await page.goto('/engine');
  await expect(page.getByRole('link', { name: 'intraktible' })).toBeVisible();
  await expect(page.getByRole('heading', { name: /Decision Engine/i })).toBeVisible();
});
