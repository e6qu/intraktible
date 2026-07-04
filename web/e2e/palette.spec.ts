// SPDX-License-Identifier: AGPL-3.0-or-later
// The ⌘K command palette: keyboard-open, filter, navigate, switch persona.
import { test, expect } from '@playwright/test';

const KEY = 'dev-sandbox-key';

test.beforeEach(async ({ page }) => {
  await page.context().request.post('/v1/login', { data: { api_key: KEY } });
});

test('opens with the keyboard, filters, and navigates', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('cmdk-trigger')).toBeVisible(); // wait for hydration
  await page.keyboard.press('ControlOrMeta+k');
  const search = page.getByRole('combobox', { name: 'Search commands' });
  await expect(search).toBeVisible();
  await search.fill('audit');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/\/audit$/);
  await expect(page.getByRole('heading', { name: /Audit log/i })).toBeVisible();
});

test('the header trigger opens it and Escape closes it', async ({ page }) => {
  await page.goto('/');
  await page.getByTestId('cmdk-trigger').click();
  const search = page.getByRole('combobox', { name: 'Search commands' });
  await expect(search).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(search).not.toBeVisible();
});

test('searches tenant entities and jumps to a flow by name', async ({ page, request }) => {
  // A uniquely-named flow so the search result is unambiguous on a reused server.
  const slug = 'pal-' + Math.random().toString(36).slice(2, 8);
  const created = await request.post('/v1/flows', {
    headers: { 'X-Api-Key': KEY },
    data: { slug, name: `Palette ${slug}` }
  });
  const { flow_id } = await created.json();

  // The palette searches the read model, which lags the create above (eventual
  // consistency). Wait for the flow to be listable before searching, so the result
  // is deterministic rather than a race against projection lag under parallel load.
  await expect
    .poll(async () => {
      const res = await request.get('/v1/flows', { headers: { 'X-Api-Key': KEY } });
      const body = await res.json();
      return (body.flows ?? []).some((f: { slug: string }) => f.slug === slug);
    })
    .toBeTruthy();

  await page.goto('/');
  await expect(page.getByTestId('cmdk-trigger')).toBeVisible();
  await page.keyboard.press('ControlOrMeta+k');
  await page.getByRole('combobox', { name: 'Search commands' }).fill(slug);

  // Entities load asynchronously; the matching option appears, then opens the flow.
  // `name` as a string is a case-insensitive substring match (no regex needed).
  const option = page.getByRole('option', { name: slug });
  await expect(option).toBeVisible();
  await option.click();
  await expect(page).toHaveURL(`/engine/${flow_id}`); // resolved against baseURL
});

test('switches the view persona', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('cmdk-trigger')).toBeVisible(); // wait for hydration
  await page.keyboard.press('ControlOrMeta+k');
  await page.getByRole('combobox', { name: 'Search commands' }).fill('view as operator');
  await page.keyboard.press('Enter');
  await expect(page.locator('html')).toHaveAttribute('data-persona', 'operator');
});

test('the index spans policies, models, and context entities', async ({ page, request }) => {
  const KEY = { 'X-Api-Key': 'dev-sandbox-key' };
  const slug = 'pal-' + Math.random().toString(36).slice(2, 8);
  const created = await request.post('/v1/flows', {
    headers: KEY,
    data: { slug, name: 'PaletteCorpus' }
  });
  const { flow_id } = await created.json();
  await request.post(`/v1/flows/${flow_id}/versions`, {
    headers: KEY,
    data: {
      graph: {
        nodes: [
          { id: 'in', type: 'input', position: { x: 0, y: 0 } },
          { id: 'out', type: 'output', position: { x: 200, y: 0 } }
        ],
        edges: [{ from: 'in', to: 'out' }]
      }
    }
  });
  await request.post('/v1/policies', {
    headers: KEY,
    data: { name: `Corpus Policy ${slug}`, flow_slug: slug }
  });
  await request.post('/v1/models', {
    headers: KEY,
    data: { name: `corpus-${slug}`, spec: { kind: 'expression', expr: '1' } }
  });
  await request.post('/v1/context/events', {
    headers: KEY,
    data: { entity_type: 'applicant', entity_id: `PAL-${slug}`, event_name: 'login', data: {} }
  });

  await page.goto('/');
  await expect(page.getByTestId('cmdk-trigger')).toBeVisible();
  await page.keyboard.press('ControlOrMeta+k');
  const box = page.getByRole('combobox', { name: 'Search commands' });
  await box.fill(`Corpus Policy ${slug}`);
  await expect(page.getByRole('option', { name: `Corpus Policy ${slug}` })).toBeVisible();
  await box.fill(`corpus-${slug}`);
  await expect(page.getByRole('option', { name: `corpus-${slug}` })).toBeVisible();
  await box.fill(`PAL-${slug}`);
  await expect(page.getByRole('option', { name: `PAL-${slug}` })).toBeVisible();
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/\/data\/applicant\/PAL-/);
});
