// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, it, expect } from 'vitest';
import { roleAtLeast } from './roles';

describe('roleAtLeast', () => {
  it('ranks viewer < operator < editor < approver < admin', () => {
    expect(roleAtLeast('admin', 'approver')).toBe(true);
    expect(roleAtLeast('approver', 'editor')).toBe(true);
    expect(roleAtLeast('editor', 'editor')).toBe(true);
    expect(roleAtLeast('operator', 'editor')).toBe(false);
    expect(roleAtLeast('viewer', 'operator')).toBe(false);
  });

  it('treats an unknown role as the floor (cannot write)', () => {
    expect(roleAtLeast('nope', 'operator')).toBe(false);
  });

  it('treats an absent role as permitted (pre-/v1/me, matches navFor)', () => {
    expect(roleAtLeast(undefined, 'admin')).toBe(true);
  });
});
