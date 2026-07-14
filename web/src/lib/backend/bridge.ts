// SPDX-License-Identifier: AGPL-3.0-or-later
// The page side of the embedded backend: boots the worker hosting the Go
// backend, routes /v1 + /healthz fetches through it, and persists the user's
// event-log delta so a reload replays their own history on top of the seed.
// Everything above this line of code is the SAME application either way — the
// bridge only swaps the transport (network socket vs worker message port).
// That includes cookies: the browser's cookie machinery never sees worker
// messages, so the bridge carries the backend's session cookie itself.
import type { WorkerRequest, WorkerResponse } from './protocol';

const DELTA_KEY = 'intraktible-event-delta';

/** The asset URLs the worker needs, resolved on the page (base-path aware). */
export interface BackendAssets {
  wasmURL: string;
  wasmExecURL: string;
  seedURL: string;
}

type Pending = {
  headerResolve: (r: Response) => void;
  headerReject: (e: Error) => void;
  controller: ReadableStreamDefaultController<Uint8Array> | null;
  setController: (c: ReadableStreamDefaultController<Uint8Array>) => void;
};

let worker: Worker | null = null;
let nextID = 1;
const pending = new Map<number, Pending>();

// The transport-level cookie jar. The Fetch spec forbids Cookie/Set-Cookie on
// script-constructed Request/Response headers, so the jar works on the raw
// protocol arrays instead — exactly what the browser does for a real socket.
const cookies = new Map<string, string>();

function storeCookies(headers: [string, string][]): void {
  for (const [name, value] of headers) {
    if (name.toLowerCase() !== 'set-cookie') continue;
    const [pair, ...attrs] = value.split(';');
    const eq = pair.indexOf('=');
    if (eq < 0) continue;
    const cookieName = pair.slice(0, eq).trim();
    const cookieValue = pair.slice(eq + 1).trim();
    const expired = attrs.some((a) => {
      const [k, v] = a.split('=');
      return k.trim().toLowerCase() === 'max-age' && Number(v) <= 0;
    });
    if (expired || cookieValue === '') cookies.delete(cookieName);
    else cookies.set(cookieName, cookieValue);
  }
}

/** Boots the embedded backend; resolves when it is serving. Throws loudly on failure. */
export function bootEmbeddedBackend(
  assets: BackendAssets,
  onProgress?: (loaded: number, total: number) => void
): Promise<void> {
  worker?.terminate(); // a retry (after clearing an incompatible delta) reboots cleanly
  worker = new Worker(new URL('./worker.ts', import.meta.url), { type: 'module' });
  const w = worker;
  const ready = new Promise<void>((resolve, reject) => {
    w.addEventListener('message', (e: MessageEvent<WorkerResponse>) => {
      const msg = e.data;
      switch (msg.kind) {
        case 'ready':
          resolve();
          break;
        case 'boot-progress':
          onProgress?.(msg.loaded, msg.total);
          break;
        case 'boot-error':
          reject(new Error(`embedded backend failed to boot: ${msg.message}`));
          break;
        case 'response': {
          const p = pending.get(msg.id);
          if (!p) throw new Error(`response for unknown request ${msg.id}`);
          storeCookies(msg.headers);
          const stream = new ReadableStream<Uint8Array>({
            start(controller) {
              p.setController(controller);
            }
          });
          p.headerResolve(
            new Response(msg.status === 204 || msg.status === 304 ? null : stream, {
              status: msg.status,
              headers: msg.headers
            })
          );
          break;
        }
        case 'chunk':
          pending.get(msg.id)?.controller?.enqueue(msg.body);
          break;
        case 'done': {
          const p = pending.get(msg.id);
          pending.delete(msg.id);
          p?.controller?.close();
          break;
        }
        case 'fetch-error': {
          const p = pending.get(msg.id);
          pending.delete(msg.id);
          const err = new Error(msg.message);
          if (p?.controller) p.controller.error(err);
          else p?.headerReject(err);
          break;
        }
        case 'delta':
          localStorage.setItem(DELTA_KEY, msg.events);
          break;
      }
    });
  });
  post({
    kind: 'boot',
    wasmURL: assets.wasmURL,
    wasmExecURL: assets.wasmExecURL,
    seedURL: assets.seedURL,
    delta: localStorage.getItem(DELTA_KEY) ?? ''
  });
  return ready;
}

/** Whether a persisted user delta is present (a returning visitor's saved session). */
export function hasEmbeddedDelta(): boolean {
  return !!localStorage.getItem(DELTA_KEY);
}

/** Clears the persisted user delta (the demo's Reset). */
export function clearEmbeddedDelta(): void {
  localStorage.removeItem(DELTA_KEY);
}

function post(m: WorkerRequest, transfer: Transferable[] = []): void {
  if (!worker) throw new Error('embedded backend not booted');
  worker.postMessage(m, transfer);
}

/** Serves one request through the embedded backend, streaming the body. */
export async function bridgeFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const req = new Request(input, init);
  const url = new URL(req.url);
  const body = req.body ? new Uint8Array(await req.arrayBuffer()) : null;
  const headers: [string, string][] = [...req.headers.entries()];
  if (cookies.size > 0) {
    headers.push(['Cookie', [...cookies].map(([k, v]) => `${k}=${v}`).join('; ')]);
  }
  const id = nextID++;
  return new Promise<Response>((resolve, reject) => {
    const p: Pending = {
      headerResolve: resolve,
      headerReject: reject,
      controller: null,
      setController(c) {
        this.controller = c;
      }
    };
    pending.set(id, p);
    post(
      {
        kind: 'fetch',
        id,
        method: req.method,
        url: url.pathname + url.search,
        headers,
        body
      },
      body ? [body.buffer] : []
    );
  });
}
