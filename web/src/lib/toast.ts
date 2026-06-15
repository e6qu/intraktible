// SPDX-License-Identifier: AGPL-3.0-or-later
// A tiny global toast store: components push success/error/info messages that the
// app-wide <Toasts> renders and auto-dismisses.

import { writable } from 'svelte/store';

export type ToastKind = 'success' | 'error' | 'info';

export interface Toast {
  id: number;
  kind: ToastKind;
  message: string;
}

export const toasts = writable<Toast[]>([]);

let nextId = 1;

function push(kind: ToastKind, message: string, ttl = 4000): void {
  const id = nextId++;
  toasts.update((list) => [...list, { id, kind, message }]);
  if (ttl > 0 && typeof setTimeout !== 'undefined') {
    setTimeout(() => dismiss(id), ttl);
  }
}

export function dismiss(id: number): void {
  toasts.update((list) => list.filter((t) => t.id !== id));
}

export const toast = {
  success: (m: string) => push('success', m),
  error: (m: string) => push('error', m, 6000),
  info: (m: string) => push('info', m)
};
