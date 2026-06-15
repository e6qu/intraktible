// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'vitest';
import { layout } from './layout';

describe('layout', () => {
  it('places a linear flow left to right by depth', () => {
    const pos = layout(
      [{ id: 'in' }, { id: 'a' }, { id: 'out' }],
      [
        { from: 'in', to: 'a' },
        { from: 'a', to: 'out' }
      ]
    );
    expect(pos.get('in')?.x).toBe(0);
    expect(pos.get('a')?.x).toBe(220);
    expect(pos.get('out')?.x).toBe(440);
    expect(pos.get('in')?.y).toBe(0);
  });

  it('stacks a branch and reconverges at the deepest column', () => {
    const pos = layout(
      [{ id: 'in' }, { id: 's' }, { id: 'yes' }, { id: 'no' }, { id: 'out' }],
      [
        { from: 'in', to: 's' },
        { from: 's', to: 'yes' },
        { from: 's', to: 'no' },
        { from: 'yes', to: 'out' },
        { from: 'no', to: 'out' }
      ]
    );
    // yes/no share a column; out is one past them.
    expect(pos.get('yes')?.x).toBe(pos.get('no')?.x);
    expect(pos.get('yes')?.y).not.toBe(pos.get('no')?.y);
    expect(pos.get('out')?.x).toBe((pos.get('yes')?.x ?? 0) + 220);
  });
});
