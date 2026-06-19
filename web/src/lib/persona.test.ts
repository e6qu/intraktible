// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, it, expect } from 'vitest';
import { PERSONAS, NAV, navFor, personaConfig, defaultPersona, type NavId } from './persona';

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
});
