// SPDX-License-Identifier: AGPL-3.0-or-later
// Shared foundation for the demo seed: the demo cast (roster) and the time helpers
// every seed module offsets from. Lives apart from store.ts so the seed modules and
// the store can both import it without a cycle.

import type { Role } from '$lib/api';

// DemoUser is one entry in the demo's cast: a named person with an RBAC role. The
// demo identity switcher (DemoBanner) lets a visitor view the app AS any of them, so
// role-gated surfaces (admin-only Model risk / Audit, maker-checker, etc.) change
// live. Seeded data (case assignees, audit actors, comment authors, approvers,
// model/agent owners) is woven from this roster so the app reads like a real team's.
export interface DemoUser {
  actor: string;
  name: string;
  role: Role;
  title: string;
}

export const ACTOR = 'ava.chen@intraktible.dev';

// The demo cast, ordered by descending privilege. The first (admin) is the default
// signed-in identity. Roles match the platform's RBAC ranks (viewer < operator <
// editor < approver < admin).
export const USERS: DemoUser[] = [
  { actor: ACTOR, name: 'Ava Chen', role: 'admin', title: 'Head of Decisioning' },
  {
    actor: 'marcus.reed@intraktible.dev',
    name: 'Marcus Reed',
    role: 'approver',
    title: 'Risk Approver'
  },
  { actor: 'priya.nair@intraktible.dev', name: 'Priya Nair', role: 'editor', title: 'Flow Author' },
  {
    actor: 'diego.santos@intraktible.dev',
    name: 'Diego Santos',
    role: 'operator',
    title: 'Case Analyst'
  },
  {
    actor: 'lena.hoff@intraktible.dev',
    name: 'Lena Hoff',
    role: 'viewer',
    title: 'Audit & Compliance'
  }
];

// Roster actor shortcuts so the seed reads like a real team (admin..viewer).
export const AVA = USERS[0].actor; // admin — Head of Decisioning (=== ACTOR)
export const MARCUS = USERS[1].actor; // approver — Risk Approver
export const PRIYA = USERS[2].actor; // editor — Flow Author
export const DIEGO = USERS[3].actor; // operator — Case Analyst
export const LENA = USERS[4].actor; // viewer — Audit & Compliance

// Anchor the seed to the REAL current time (floored to the hour for stable reads within
// a session), so every ago()/ahead() offset stays correctly relative. A fixed past date
// drifts: "expiring soon" pre-approvals and scheduled deploys render as already expired
// once the real clock passes it.
export const now = (() => {
  const d = new Date();
  d.setMinutes(0, 0, 0);
  return d;
})();

export function ago(hours: number): string {
  return new Date(now.getTime() - hours * 3600_000).toISOString();
}

export function ahead(days: number): string {
  return new Date(now.getTime() + days * 86400_000).toISOString();
}
