// SPDX-License-Identifier: AGPL-3.0-or-later
// Relative-timestamp helpers + a single shared clock. RelativeTime components
// subscribe to `now` so every "2m ago" stays fresh with ONE timer for the whole
// app (the readable only ticks while something is subscribed).

import { readable } from 'svelte/store';

export const now = readable(Date.now(), (set) => {
  const id = setInterval(() => set(Date.now()), 30_000);
  return () => clearInterval(id);
});

// relativeTime renders an ISO timestamp as a short, human "… ago", "in …" (future), or
// "just now". `ref` is the comparison instant (injectable for tests / driven by `now`).
export function relativeTime(iso: string, ref: number = Date.now()): string {
  if (!iso) return '';
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return '';
  const sec = Math.round((ref - t) / 1000);
  // FUTURE timestamps (a pre-approval's expiry, a scheduled deploy) render "in X" — any
  // future time previously fell through the `< 45` guard and read "just now", so an
  // expiry 20 days out looked already-expired. A small future delta is still clock skew.
  if (sec < -45) return `in ${magnitude(-sec)}`;
  if (sec < 45) return 'just now';
  return `${magnitude(sec)} ago`;
}

// magnitude formats a positive second count as a coarse human duration (no direction).
function magnitude(sec: number): string {
  if (sec < 90) return '1m';
  const min = Math.round(sec / 60);
  if (min < 60) return `${min}m`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr}h`;
  const day = Math.round(hr / 24);
  if (day < 7) return `${day}d`;
  const wk = Math.round(day / 7);
  if (wk < 5) return `${wk}w`;
  const mo = Math.round(day / 30);
  if (mo < 12) return `${mo}mo`;
  return `${Math.round(day / 365)}y`;
}

// absoluteTime is the full locale timestamp (used as the hover title).
export function absoluteTime(iso: string): string {
  if (!iso) return '';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString();
}
