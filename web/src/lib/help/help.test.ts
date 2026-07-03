// SPDX-License-Identifier: AGPL-3.0-or-later
// This test walks the source route tree, so its fs path is computed, not literal —
// the non-literal-fs lint doesn't apply to a build-time source scanner.
/* eslint-disable security/detect-non-literal-fs-filename */
import { describe, it, expect } from 'vitest';
import { readdirSync } from 'node:fs';
import { join } from 'node:path';
import { HELP, helpFor } from './registry';

// Routes whose guide has no journeys. Only /login qualifies: it is a single
// form with nothing to walk step by step. Every other page documents each flow.
const NO_JOURNEYS = new Set(['/login']);

// Discover every route that has a +page.svelte, mapped to its route id, by walking
// src/routes (so a new page without a guide entry fails CI).
function routeIds(dir = 'src/routes', base = ''): string[] {
  const out: string[] = [];
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (e.isDirectory()) {
      out.push(...routeIds(join(dir, e.name), `${base}/${e.name}`));
    } else if (e.name === '+page.svelte') {
      out.push(base === '' ? '/' : base);
    }
  }
  return out;
}

describe('in-app page guide', () => {
  it('every page has a guide entry', () => {
    const missing = routeIds().filter((id) => !helpFor(id));
    expect(missing, `routes missing a HELP entry: ${missing.join(', ')}`).toEqual([]);
  });

  it('has no stale entries for routes that no longer exist', () => {
    const routes = new Set(routeIds());
    const stale = [...HELP.keys()].filter((id) => !routes.has(id));
    expect(stale, `HELP entries without a route: ${stale.join(', ')}`).toEqual([]);
  });

  it('every page explains each flow step by step (≥1 journey of ≥3 steps)', () => {
    for (const [id, h] of HELP) {
      if (NO_JOURNEYS.has(id)) continue;
      const journeys = h.journeys ?? [];
      expect(journeys.length, `${id} journeys`).toBeGreaterThanOrEqual(1);
      const walkable = journeys.filter((j) => j.steps.length >= 3);
      expect(walkable.length, `${id} needs a journey with ≥3 steps`).toBeGreaterThanOrEqual(1);
    }
  });

  it('keeps guide content within the style-guide bounds (scannable, not verbose)', () => {
    for (const [id, h] of HELP) {
      expect(h.title.length, `${id} title`).toBeGreaterThan(0);
      expect(h.summary.split(/\s+/).length, `${id} summary word count`).toBeLessThanOrEqual(45);
      expect(h.capabilities.length, `${id} capabilities`).toBeGreaterThanOrEqual(2);
      expect(h.capabilities.length, `${id} capabilities`).toBeLessThanOrEqual(6);
      for (const j of h.journeys ?? []) {
        expect(j.steps.length, `${id} journey "${j.name}" steps`).toBeGreaterThanOrEqual(3);
        expect(j.steps.length, `${id} journey "${j.name}" steps`).toBeLessThanOrEqual(7);
      }
      expect((h.journeys ?? []).length, `${id} journeys`).toBeLessThanOrEqual(10);
    }
  });
});
