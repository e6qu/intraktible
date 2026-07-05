// SPDX-License-Identifier: AGPL-3.0-or-later
// Per-navigation record of the API calls the client layer made, powering the
// "Export for AI" document's "Underlying API calls" section. api.ts's default
// fetcher calls record() after every response; the layout resets the buffer on
// route change, so the buffer always describes the current page's visit. Sits
// above transport, so it works identically against the native backend and the
// wasm demo's bridged fetch.
import { writable } from 'svelte/store';

export interface RecordedCall {
  method: string;
  path: string; // pathname only — no host, no query
  status: number;
}

// Ring-buffer cap: a page that polls can't grow the buffer without bound.
const CAP = 50;

export const recordedCalls = writable<RecordedCall[]>([]);

export function record(call: RecordedCall): void {
  recordedCalls.update((calls) => [...calls, call].slice(-CAP));
}

export function resetRecorder(): void {
  recordedCalls.set([]);
}
