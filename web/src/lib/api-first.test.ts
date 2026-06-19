// SPDX-License-Identifier: AGPL-3.0-or-later
// Enforces the API-first guarantee (see docs/API-FIRST.md): the UI is one client of
// the public /v1 API with no UI-only backdoors. These tests fail if a change adds a
// raw fetch(), a SvelteKit server route, or a non-/v1 endpoint in api.ts.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

function walk(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...walk(p));
    else out.push(p);
  }
  return out;
}

const srcFiles = walk('src').filter(
  (f) => /\.(ts|svelte)$/.test(f) && !/\.(test|spec)\.ts$/.test(f)
);

describe('API-first guarantee', () => {
  it('routes every network call through api.ts (no raw fetch())', () => {
    // All calls go through the injected `fetcher(...)`; a literal `fetch(` is a
    // direct network call that bypasses the typed api.ts seam. (`fetcher(` and the
    // default param `= fetch` have no `(` right after `fetch`, so they don't match.)
    const offenders = srcFiles.filter(
      (f) => f !== join('src', 'lib', 'api.ts') && /[^a-zA-Z]fetch\(/.test(readFileSync(f, 'utf8'))
    );
    expect(offenders).toEqual([]);
  });

  it('has no SvelteKit server routes that could bypass the public API', () => {
    const serverRoutes = srcFiles.filter((f) =>
      /\+(page|layout)\.server\.ts$|\+server\.ts$/.test(f)
    );
    expect(serverRoutes).toEqual([]);
  });

  it('only calls /v1 endpoints from api.ts', () => {
    const api = readFileSync(join('src', 'lib', 'api.ts'), 'utf8');
    const paths = [...api.matchAll(/fetcher\(\s*[`'"](\/[^`'"$]*)/g)].map((m) => m[1]);
    expect(paths.length).toBeGreaterThan(10); // sanity: we actually scanned the calls
    expect(paths.filter((p) => !p.startsWith('/v1'))).toEqual([]);
  });
});
