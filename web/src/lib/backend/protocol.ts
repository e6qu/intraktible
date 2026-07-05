// SPDX-License-Identifier: AGPL-3.0-or-later
// The message protocol between the page and the worker hosting the Go backend
// compiled to wasm. Pure transport: the worker serves the SAME handler the
// native binary listens on — no demo-specific application code on either side.

/** Page → worker. */
export type WorkerRequest =
  | {
      kind: 'boot';
      /**
       * Every asset URL is resolved on the PAGE (where SvelteKit's base path is
       * known) — the worker bundle lives under _app/ so nothing may resolve
       * relative to the worker's own location.
       */
      wasmURL: string;
      /** Go's JS runtime shim (defines globalThis.Go). */
      wasmExecURL: string;
      /** The seed event-log URL. */
      seedURL: string;
      /** User-delta events persisted from earlier sessions (JSON envelope array), '' for none. */
      delta: string;
    }
  | {
      kind: 'fetch';
      id: number;
      method: string;
      /** Path + query (the authority is the embedded backend itself). */
      url: string;
      headers: [string, string][];
      body: Uint8Array | null;
    }
  | { kind: 'export-delta' };

/** Worker → page. */
export type WorkerResponse =
  | { kind: 'ready' }
  | {
      /** Wasm download progress for the boot splash (total 0 when unknown). */
      kind: 'boot-progress';
      loaded: number;
      total: number;
    }
  | { kind: 'boot-error'; message: string }
  | {
      kind: 'response';
      id: number;
      status: number;
      headers: [string, string][];
    }
  | { kind: 'chunk'; id: number; body: Uint8Array }
  | { kind: 'done'; id: number }
  | { kind: 'fetch-error'; id: number; message: string }
  | {
      /** An event was appended past the seed head — the page persists the delta. */
      kind: 'delta';
      /** JSON array of every envelope appended after the seed (full snapshot each time). */
      events: string;
    };
