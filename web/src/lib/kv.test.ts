// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'vitest';
import { displayEntries } from './kv';

describe('displayEntries', () => {
  it('flattens primitives to text and nested values to JSON', () => {
    expect(displayEntries({ fico: 700, name: 'ada', active: true })).toEqual([
      ['fico', '700'],
      ['name', 'ada'],
      ['active', 'true']
    ]);
    expect(displayEntries({ nested: { a: 1 }, list: [1, 2] })).toEqual([
      ['nested', '{"a":1}'],
      ['list', '[1,2]']
    ]);
  });

  it('yields nothing for non-objects', () => {
    expect(displayEntries(null)).toEqual([]);
    expect(displayEntries('x')).toEqual([]);
    expect(displayEntries([1, 2])).toEqual([]);
    expect(displayEntries(undefined)).toEqual([]);
  });
});
