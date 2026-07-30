// SPDX-License-Identifier: AGPL-3.0-or-later
import { test, expect } from '@playwright/test';
import { TEMPLATES } from '../src/lib/templates';

const KEY = 'dev-sandbox-key';

// Each starter template must be a VALID canonical source on the real backend.
// Authoring import compiles and validates before creating the durable draft, so
// this catches a bad node type, dangling edge, or cycle before a user hits it.
test('every starter template imports as a valid flow', async ({ request }) => {
  for (const t of TEMPLATES) {
    const slug = `tmpl-${t.id}-${Math.random().toString(36).slice(2, 8)}`;
    const res = await request.post('/v1/authoring/import', {
      headers: {
        'X-Api-Key': KEY,
        'Idempotency-Key': `template-${slug}`
      },
      data: {
        ...t.doc,
        slug,
        format_version: 'intraktible.authoring/v1',
        kind: 'flow'
      }
    });
    expect(
      res.ok(),
      `template "${t.name}" failed to import: ${res.status()} ${await res.text()}`
    ).toBeTruthy();
  }
});
