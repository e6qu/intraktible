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

// roleAtLeast reports whether `role` ranks at or above `min`. An unknown or absent
// role (signed out, or before /v1/me resolves) ranks BELOW every requirement, so a
// write control is disabled and an admin-only page is hidden until a real role is
// known. Fail closed: briefly disabling a control for an admin on first paint is far
// better than offering a viewer (or a signed-out visitor) an affordance the backend
// will 403, or dead-ending them on an admin page they can never load.
export function roleAtLeast(role: string | undefined, min: RoleName): boolean {
  return (RANK.get(role ?? '') ?? 0) >= (RANK.get(min) ?? 99);
}
