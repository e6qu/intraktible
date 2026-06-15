// SPDX-License-Identifier: AGPL-3.0-or-later
// Light/dark theme: persisted in localStorage, defaulting to the OS preference.
// The chosen theme is applied as <html data-theme="…"> so app.css swaps tokens,
// and mirrored into a store so components (e.g. the Svelte Flow canvas) can react.

import { writable } from 'svelte/store';

export type Theme = 'light' | 'dark';

const KEY = 'intraktible-theme';

// theme is the reactive current theme (kept in sync by initTheme/setTheme).
export const theme = writable<Theme>('light');

// systemTheme reads the OS colour-scheme preference (light when unknown).
function systemTheme(): Theme {
  if (typeof window !== 'undefined' && window.matchMedia) {
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }
  return 'light';
}

// resolveTheme is the stored choice, or the OS preference when none is stored.
export function resolveTheme(): Theme {
  if (typeof localStorage !== 'undefined') {
    const stored = localStorage.getItem(KEY);
    if (stored === 'light' || stored === 'dark') return stored;
  }
  return systemTheme();
}

// applyTheme sets the document attribute that drives the CSS variables.
function applyTheme(t: Theme): void {
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('data-theme', t);
  }
}

// setTheme persists, applies, and publishes a theme.
export function setTheme(t: Theme): void {
  if (typeof localStorage !== 'undefined') {
    localStorage.setItem(KEY, t);
  }
  applyTheme(t);
  theme.set(t);
}

// initTheme resolves and publishes the active theme (call once on mount).
export function initTheme(): Theme {
  const t = resolveTheme();
  applyTheme(t);
  theme.set(t);
  return t;
}

// toggleTheme flips between light and dark, persisting the result.
export function toggleTheme(current: Theme): Theme {
  const next: Theme = current === 'dark' ? 'light' : 'dark';
  setTheme(next);
  return next;
}
