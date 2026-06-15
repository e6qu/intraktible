// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'vitest';
import {
  asText,
  asNum,
  asCsv,
  fromCsv,
  cleanConfig,
  asCellText,
  parseCell,
  addUniqueEdge
} from './nodeconfig';

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

  it('asNum renders only real numbers', () => {
    expect(asNum(7)).toBe('7');
    expect(asNum(0)).toBe('0');
    expect(asNum(NaN)).toBe('');
    expect(asNum('7')).toBe('');
    expect(asNum(undefined)).toBe('');
  });

  it('asCellText / parseCell preserve literal types', () => {
    expect(asCellText('approve')).toBe('approve');
    expect(asCellText(7)).toBe('7');
    expect(asCellText(true)).toBe('true');
    expect(asCellText(undefined)).toBe('');
    expect(asCellText(null)).toBe('');
    expect(parseCell('7')).toBe(7);
    expect(parseCell('true')).toBe(true);
    expect(parseCell('"x"')).toBe('x');
    expect(parseCell('approve')).toBe('approve'); // not JSON → plain string
    expect(parseCell('')).toBe('');
  });

  it('addUniqueEdge appends without duplicating', () => {
    const e0 = addUniqueEdge([], 'n1', 'n2');
    expect(e0).toEqual([{ from: 'n1', to: 'n2' }]);
    // Duplicate of an unbranched edge is ignored.
    expect(addUniqueEdge(e0, 'n1', 'n2')).toBe(e0);
    // Missing endpoints are a no-op.
    expect(addUniqueEdge(e0, '', 'n2')).toBe(e0);
    // A distinct edge appends.
    expect(addUniqueEdge(e0, 'n2', 'n3')).toHaveLength(2);
  });
});
