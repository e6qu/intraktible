// SPDX-License-Identifier: AGPL-3.0-or-later
// Relative-timestamp helpers + a single shared clock. RelativeTime components
// subscribe to `now` so every "2m ago" stays fresh with ONE timer for the whole
// app (the readable only ticks while something is subscribed).

import { readable } from 'svelte/store';

export const now = readable(Date.now(), (set) => {
  const id = setInterval(() => set(Date.now()), 30_000);
  return () => clearInterval(id);
});

// relativeTime renders an ISO timestamp as a short, human "… ago" (or "just now").
// `ref` is the comparison instant (injectable for tests / driven by `now`).
export function relativeTime(iso: string, ref: number = Date.now()): string {
  if (!iso) return '';
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return '';
  const sec = Math.round((ref - t) / 1000);
  if (sec < 45) return 'just now'; // also covers small clock skew (future)
  if (sec < 90) return '1m ago';
  const min = Math.round(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.round(hr / 24);
  if (day < 7) return `${day}d ago`;
  const wk = Math.round(day / 7);
  if (wk < 5) return `${wk}w ago`;
  const mo = Math.round(day / 30);
  if (mo < 12) return `${mo}mo ago`;
  return `${Math.round(day / 365)}y ago`;
}

// absoluteTime is the full locale timestamp (used as the hover title).
export function absoluteTime(iso: string): string {
  if (!iso) return '';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString();
}
