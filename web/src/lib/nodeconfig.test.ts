// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'vitest';
import { asText, asCsv, fromCsv, cleanConfig } from './nodeconfig';

describe('nodeconfig helpers', () => {
  it('asText renders strings, numbers, and empties', () => {
    expect(asText('hi')).toBe('hi');
    expect(asText(3)).toBe('3');
    expect(asText(null)).toBe('');
    expect(asText(undefined)).toBe('');
  });

  it('asCsv / fromCsv round-trip a list', () => {
    expect(asCsv(['a', 'b'])).toBe('a, b');
    expect(asCsv('not-an-array')).toBe('');
    expect(fromCsv(' a , b ,, c ')).toEqual(['a', 'b', 'c']);
    expect(fromCsv('')).toEqual([]);
  });

  it('cleanConfig drops empty fields', () => {
    expect(
      cleanConfig({
        condition: 'x',
        empty: '',
        missing: null,
        none: undefined,
        list: [],
        keep: ['y']
      })
    ).toEqual({ condition: 'x', keep: ['y'] });
  });
});
