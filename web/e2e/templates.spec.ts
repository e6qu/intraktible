// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';
import { TEMPLATES } from '../src/lib/templates';

const KEY = 'dev-sandbox-key';

// Each starter template must be a VALID flow on the real backend — the demo's import is
// permissive, but POST /v1/flows/import runs ValidateGraph, so this is what catches an
// authoring mistake (bad node type, dangling edge, cycle) before a user hits it.
test('every starter template imports as a valid flow', async ({ request }) => {
  for (const t of TEMPLATES) {
    const slug = `tmpl-${t.id}-${Math.random().toString(36).slice(2, 8)}`;
    const res = await request.post('/v1/flows/import', {
      headers: { 'X-Api-Key': KEY },
      data: { ...t.doc, slug }
    });
    expect(
      res.ok(),
      `template "${t.name}" failed to import: ${res.status()} ${await res.text()}`
    ).toBeTruthy();
  }
});
