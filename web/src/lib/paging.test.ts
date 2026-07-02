// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, it, expect } from 'vitest';
import { withOffset } from './paging';

describe('withOffset', () => {
  it('adds the offset to an unfiltered query', () => {
    expect(withOffset(new URLSearchParams(), 100)).toBe('offset=100');
  });

  it('preserves the applied filter params untouched', () => {
    const qs = withOffset(new URLSearchParams('stream=decision&actor=dev'), 200);
    const p = new URLSearchParams(qs);
    expect(p.get('stream')).toBe('decision');
    expect(p.get('actor')).toBe('dev');
    expect(p.get('offset')).toBe('200');
  });

  it('replaces an existing offset instead of stacking one', () => {
    const qs = withOffset(new URLSearchParams('offset=100&env=sandbox'), 200);
    expect(new URLSearchParams(qs).getAll('offset')).toEqual(['200']);
    expect(new URLSearchParams(qs).get('env')).toBe('sandbox');
  });

  it('drops the offset param entirely on page one', () => {
    expect(withOffset(new URLSearchParams('offset=100&q=abc'), 0)).toBe('q=abc');
    expect(withOffset(new URLSearchParams('offset=100'), 0)).toBe('');
  });

  it('does not mutate the caller’s params (the live URL)', () => {
    const orig = new URLSearchParams('offset=100');
    withOffset(orig, 300);
    expect(orig.get('offset')).toBe('100');
  });
});
