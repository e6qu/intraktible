// SPDX-License-Identifier: AGPL-3.0-or-later
// Open/close state for the global ⌘K command palette, so the header trigger, the
// keyboard shortcut (in CommandPalette.svelte), and the palette itself all share
// one source of truth.

import { writable } from 'svelte/store';

export const paletteOpen = writable(false);

export function openPalette(): void {
  paletteOpen.set(true);
}
export function closePalette(): void {
  paletteOpen.set(false);
}
export function togglePalette(): void {
  paletteOpen.update((v) => !v);
}
