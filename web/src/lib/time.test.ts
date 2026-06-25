// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, it, expect } from 'vitest';
import { relativeTime, absoluteTime } from './time';

const REF = Date.parse('2026-06-17T12:00:00Z');
const ago = (ms: number) => new Date(REF - ms).toISOString();
const S = 1000,
  M = 60 * S,
  H = 60 * M,
  D = 24 * H;

describe('relativeTime', () => {
  it('handles empty / invalid input', () => {
    expect(relativeTime('', REF)).toBe('');
    expect(relativeTime('not-a-date', REF)).toBe('');
  });

  it('buckets recent times', () => {
    expect(relativeTime(ago(5 * S), REF)).toBe('just now');
    expect(relativeTime(ago(60 * S), REF)).toBe('1m ago');
    expect(relativeTime(ago(5 * M), REF)).toBe('5m ago');
    expect(relativeTime(ago(3 * H), REF)).toBe('3h ago');
    expect(relativeTime(ago(2 * D), REF)).toBe('2d ago');
    expect(relativeTime(ago(10 * D), REF)).toBe('1w ago');
    expect(relativeTime(ago(40 * D), REF)).toBe('1mo ago');
    expect(relativeTime(ago(400 * D), REF)).toBe('1y ago');
  });

  it('treats a small future delta as clock-skew "just now" but renders real future times "in X"', () => {
    expect(relativeTime(ago(-30 * S), REF)).toBe('just now');
    expect(relativeTime(ago(-3 * H), REF)).toBe('in 3h');
    expect(relativeTime(ago(-2 * D), REF)).toBe('in 2d');
    expect(relativeTime(ago(-20 * D), REF)).toBe('in 3w');
  });
});

describe('absoluteTime', () => {
  it('renders a locale string, or empty for bad input', () => {
    expect(absoluteTime(ago(0))).not.toBe('');
    expect(absoluteTime('')).toBe('');
    expect(absoluteTime('nope')).toBe('');
  });
});
