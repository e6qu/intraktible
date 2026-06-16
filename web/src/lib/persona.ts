// SPDX-License-Identifier: AGPL-3.0-or-later
// Persona: who is looking. The same data, re-skinned and re-prioritised for the
// viewer. Persisted in localStorage and applied as <html data-persona="…"> so
// app.css can swap the type scale, accent, and density per persona; mirrored into
// a store so components (e.g. the landing dashboard) can branch on it. Persona is
// orthogonal to light/dark theme — every persona works in both.

import { writable } from 'svelte/store';

export type Persona = 'builder' | 'operator' | 'showcase';

const KEY = 'intraktible-persona';

// The set of personas, in switcher order, with the copy the UI shows.
export const PERSONAS: { id: Persona; label: string; blurb: string }[] = [
  { id: 'builder', label: 'Builder', blurb: 'Developer & workflow maintainer' },
  { id: 'operator', label: 'Operator', blurb: 'Risk & operations manager' },
  { id: 'showcase', label: 'Showcase', blurb: 'Stakeholder & executive view' }
];

// persona is the reactive current persona (kept in sync by initPersona/setPersona).
export const persona = writable<Persona>('builder');

function isPersona(v: string | null): v is Persona {
  return v === 'builder' || v === 'operator' || v === 'showcase';
}

// resolvePersona is the stored choice, defaulting to Builder (the maintainer view).
export function resolvePersona(): Persona {
  if (typeof localStorage !== 'undefined') {
    const stored = localStorage.getItem(KEY);
    if (isPersona(stored)) return stored;
  }
  return 'builder';
}

function applyPersona(p: Persona): void {
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('data-persona', p);
  }
}

// setPersona persists, applies, and publishes a persona.
export function setPersona(p: Persona): void {
  if (typeof localStorage !== 'undefined') {
    localStorage.setItem(KEY, p);
  }
  applyPersona(p);
  persona.set(p);
}

// initPersona resolves and publishes the active persona (call once on mount).
export function initPersona(): Persona {
  const p = resolvePersona();
  applyPersona(p);
  persona.set(p);
  return p;
}
