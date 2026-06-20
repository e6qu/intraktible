// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, it, expect } from 'vitest';
import {
  PERSONAS,
  NAV,
  navFor,
  personaConfig,
  personaLens,
  defaultPersona,
  type NavId
} from './persona';

describe('persona config', () => {
  it('every persona navigates only to catalog entries', () => {
    for (const p of PERSONAS) {
      for (const id of p.nav) {
        expect(NAV.has(id)).toBe(true);
      }
    }
  });

  it('every persona surfaces at least one primary action with a route', () => {
    for (const p of PERSONAS) {
      expect(p.actions.length).toBeGreaterThan(0);
      for (const a of p.actions) {
        expect(a.href).toMatch(/^\//);
        expect(a.label.length).toBeGreaterThan(0);
      }
    }
  });

  it('the default persona is in the set and is the first entry', () => {
    expect(PERSONAS[0].id).toBe(defaultPersona);
    expect(PERSONAS.some((p) => p.id === defaultPersona)).toBe(true);
  });

  it('navFor returns the persona ordered subset with term relabels applied', () => {
    // Developer relabels Decisions -> Traces and omits Cases.
    const nav = navFor('developer');
    const ids = nav.map((n) => n.id);
    expect(ids).toEqual(['decisions', 'engine', 'keys', 'agents', 'data', 'audit']);
    expect(nav.find((n) => n.id === 'decisions')?.label).toBe('Traces');
    expect(ids).not.toContain('cases');
  });

  it('different personas compose different navigation', () => {
    const builder = navFor('builder').map((n) => n.id);
    const operator = navFor('operator').map((n) => n.id);
    expect(builder).not.toEqual(operator);
  });

  it('personaConfig falls back for an unknown id', () => {
    const cfg = personaConfig('nope' as unknown as Parameters<typeof personaConfig>[0]);
    expect(cfg.id).toBe(defaultPersona);
  });

  it('NAV ids are self-consistent', () => {
    for (const [id, item] of NAV) {
      expect(item.id).toBe(id as NavId);
      expect(item.href).toMatch(/^\//);
    }
  });

  it('a persona lens re-prioritises a list surface to the role-relevant slice', () => {
    // Operators land on their open review queue; developers on failed traces;
    // product lands on the challenger (experiment) arm.
    expect(personaLens('operator').cases).toBe('needs_review');
    expect(personaLens('developer').decisions?.status).toBe('failed');
    expect(personaLens('product').decisions?.variant).toBe('challenger');
    // A persona without a lens for a surface gets the full, unfiltered list.
    expect(personaLens('builder').cases).toBeUndefined();
    expect(personaLens('builder').decisions).toBeUndefined();
    expect(personaLens('operator').decisions).toBeUndefined();
  });

  it('every declared lens value is a real domain enum member', () => {
    const cases = new Set(['needs_review', 'in_progress', 'completed']);
    const decisionStatus = new Set(['started', 'completed', 'failed']);
    const variant = new Set(['champion', 'challenger']);
    const env = new Set(['sandbox', 'staging', 'production']);
    for (const p of PERSONAS) {
      if (p.lens?.cases) expect(cases.has(p.lens.cases)).toBe(true);
      const d = p.lens?.decisions;
      if (d?.status) expect(decisionStatus.has(d.status)).toBe(true);
      if (d?.variant) expect(variant.has(d.variant)).toBe(true);
      if (d?.env) expect(env.has(d.env)).toBe(true);
    }
  });
});
