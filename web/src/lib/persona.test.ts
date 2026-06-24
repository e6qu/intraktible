// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, it, expect } from 'vitest';
import {
  PERSONAS,
  NAV,
  navFor,
  actionsFor,
  isAdminOnlyRoute,
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
        // An action targets either an in-app route or an absolute external URL
        // (e.g. the developer persona's docs link).
        expect(a.href).toMatch(/^(\/|https?:\/\/)/);
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
    expect(ids).toEqual([
      'decisions',
      'engine',
      'keys',
      'agents',
      'data',
      'observability',
      'audit'
    ]);
    expect(nav.find((n) => n.id === 'decisions')?.label).toBe('Traces');
    expect(ids).not.toContain('cases');
  });

  it('drops admin-only nav items for a non-admin role', () => {
    // Manager nav includes the admin-gated mrm + audit.
    const asAdmin = navFor('manager', 'admin').map((n) => n.id);
    expect(asAdmin).toContain('mrm');
    expect(asAdmin).toContain('audit');
    // A non-admin manager must not see admin-only surfaces (avoids a 403 dead-end).
    const asOperator = navFor('manager', 'operator').map((n) => n.id);
    expect(asOperator).not.toContain('mrm');
    expect(asOperator).not.toContain('audit');
    expect(asOperator).toContain('cases'); // non-admin items remain
    // Omitting role (pre-/v1/me) shows the full set, matching prior behavior.
    expect(navFor('manager').map((n) => n.id)).toContain('mrm');
  });

  it('actionsFor gates admin-only home actions by role, like navFor', () => {
    // The manager persona surfaces an Audit shortcut among its primary actions.
    const adminHrefs = actionsFor('manager', 'admin').map((a) => a.href);
    expect(adminHrefs).toContain('/audit');
    // A non-admin manager must not see the admin-only shortcut on the home.
    const opHrefs = actionsFor('manager', 'operator').map((a) => a.href);
    expect(opHrefs).not.toContain('/audit');
    expect(opHrefs).not.toContain('/mrm');
    expect(opHrefs.length).toBeGreaterThan(0); // non-admin actions remain
    // Omitting role (pre-/v1/me) keeps the full set.
    expect(actionsFor('manager').map((a) => a.href)).toContain('/audit');
  });

  it('gates keys/mrm/audit as admin-only in nav and via isAdminOnlyRoute', () => {
    // keys is now admin-gated alongside mrm + audit.
    expect(isAdminOnlyRoute('/keys')).toBe(true);
    expect(isAdminOnlyRoute('/mrm')).toBe(true);
    expect(isAdminOnlyRoute('/audit')).toBe(true);
    expect(isAdminOnlyRoute('/decisions')).toBe(false);
    // The developer persona nav includes keys for an admin but not for a non-admin.
    expect(navFor('developer', 'admin').map((n) => n.id)).toContain('keys');
    expect(navFor('developer', 'operator').map((n) => n.id)).not.toContain('keys');
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
    expect(personaLens('operator').cases?.status).toBe('needs_review');
    expect(personaLens('operator').cases?.sort).toBe('urgency'); // queue ordered by urgency
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
    const caseSort = new Set(['urgency', 'recent']);
    for (const p of PERSONAS) {
      if (p.lens?.cases?.status) expect(cases.has(p.lens.cases.status)).toBe(true);
      if (p.lens?.cases?.sort) expect(caseSort.has(p.lens.cases.sort)).toBe(true);
      const d = p.lens?.decisions;
      if (d?.status) expect(decisionStatus.has(d.status)).toBe(true);
      if (d?.variant) expect(variant.has(d.variant)).toBe(true);
      if (d?.env) expect(env.has(d.env)).toBe(true);
    }
  });

  it('persona-home personas declare a role-specific panel (not a shared feed)', () => {
    expect(personaConfig('manager').homePanel).toBe('approvals');
    expect(personaConfig('developer').homePanel).toBe('failing');
    expect(personaConfig('product').homePanel).toBe('experiment');
    const kinds = new Set(['recent', 'approvals', 'failing', 'experiment']);
    for (const p of PERSONAS) {
      if (p.homePanel) expect(kinds.has(p.homePanel)).toBe(true);
      // a persona that lands on the config-driven home must pick a panel, so the three
      // don't collapse back into the same generic feed.
      if (p.home === 'persona') expect(p.homePanel).toBeDefined();
    }
  });
});
