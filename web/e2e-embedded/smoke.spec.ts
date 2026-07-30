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
  await expect(page.getByRole('heading', { name: /Flows/i })).toBeVisible();
});

test('the embedded artifact serves durable collaborative authoring', async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
  const slug = `embedded-draft-${Math.random().toString(36).slice(2, 9)}`;
  await page.goto('/engine');
  await page.getByLabel('slug', { exact: true }).fill(slug);
  await page.getByLabel('name', { exact: true }).fill('Embedded collaborative draft');
  await page.getByRole('button', { name: 'Create flow' }).click();
  await expect(page.getByTestId('draft-save-state')).toHaveText('Saved · r1');

  await page.getByLabel('Review title').fill(`Reviewed ${slug}`);
  await expect(page.getByTestId('draft-save-state')).toHaveText('Saved · r2');
  await page.reload();
  await expect(page.getByLabel('Review title')).toHaveValue(`Reviewed ${slug}`);
});

// The launch origin must fail closed for a browser with no session. This asserts
// it the way an external SSO validator observes it — the response to a real
// navigation, before any client JS runs — because a client-side redirect looks
// identical to a user and completely different to anything reading the wire.
test('an anonymous browser navigation is redirected, not handed the app shell', async ({
  page
}) => {
  const response = await page.goto('/');

  // The redirect is server-side, so the navigation's own response chain contains
  // it rather than a 200 that later rewrites the location from script.
  const chain = response?.request().redirectedFrom();
  expect(chain, 'GET / must redirect server-side for an anonymous browser').toBeTruthy();
  expect(page.url()).not.toMatch(/\/$/);

  // And the landed page is the sign-in surface, not the application.
  await expect(page.getByRole('heading', { name: 'intraktible' })).toHaveCount(0);
});
