// SPDX-License-Identifier: AGPL-3.0-or-later
// Open/close state for the keyboard-shortcuts (?) overlay, shared so any component
// can open it; the overlay itself owns the global key handling.

import { writable } from 'svelte/store';

export const shortcutsOpen = writable(false);

export function openShortcuts(): void {
  shortcutsOpen.set(true);
}
export function closeShortcuts(): void {
  shortcutsOpen.set(false);
}

// g-then-key navigation targets (GitHub-style). Kept here so the overlay can both
// drive and document them from one source.
export const GO_NAV: { key: string; href: string; label: string }[] = [
  { key: 'h', href: '/', label: 'Home dashboard' },
  { key: 'e', href: '/engine', label: 'Decision Engine' },
  { key: 'p', href: '/policies', label: 'Policies' },
  { key: 'd', href: '/decisions', label: 'Decisions' },
  { key: 'x', href: '/data', label: 'Context data' },
  { key: 'c', href: '/cases', label: 'Cases' },
  { key: 'a', href: '/agents', label: 'Agents' },
  { key: 'u', href: '/audit', label: 'Audit log' }
];
