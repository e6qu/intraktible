// SPDX-License-Identifier: AGPL-3.0-or-later
// RBAC rank helper for gating write controls in the UI by the signed-in role
// (viewer < operator < editor < approver < admin, from /v1/me). Destination pages
// also gate server-side; hiding/disabling affordances a role can't use gives a
// viewer/operator a coherent read-mostly experience instead of buttons that 403 on
// click. A Map keeps variable-key lookups clear of the object-injection lint.
export type RoleName = 'viewer' | 'operator' | 'editor' | 'approver' | 'admin';

const RANK = new Map<string, number>([
  ['viewer', 1],
  ['operator', 2],
  ['editor', 3],
  ['approver', 4],
  ['admin', 5]
]);

// roleAtLeast reports whether `role` ranks at or above `min`. An unknown/absent role
// (e.g. before /v1/me resolves) is treated as permitted, matching navFor's behavior
// — so controls aren't briefly disabled for an admin on first paint.
export function roleAtLeast(role: string | undefined, min: RoleName): boolean {
  if (role === undefined) return true;
  return (RANK.get(role) ?? 0) >= (RANK.get(min) ?? 99);
}
