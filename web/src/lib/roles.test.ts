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

  it('treats an unresolved role as NOT permitted (signed out / pre-/v1/me, fail closed)', () => {
    expect(roleAtLeast(undefined, 'admin')).toBe(false);
    expect(roleAtLeast(undefined, 'operator')).toBe(false);
    expect(roleAtLeast(undefined, 'viewer')).toBe(false);
  });

  it('permits a resolved role that meets the bar', () => {
    expect(roleAtLeast('operator', 'operator')).toBe(true);
    expect(roleAtLeast('admin', 'admin')).toBe(true);
  });
});
