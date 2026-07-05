// SPDX-License-Identifier: AGPL-3.0-or-later
// The Web Worker hosting the real Go backend compiled to wasm. It boots the
// Go runtime, replays the seed event log (plus any user delta from earlier
// sessions), then serves every request through the exact handler the native
// binary mounts — same routes, same RBAC, same engine. No fallbacks: a boot
// or request failure surfaces loudly to the page.
import type { WorkerRequest, WorkerResponse } from './protocol';
import { registerSimulatedAI } from './ai-sim';

// The contract the Go wasm main registers on the worker's global scope.
interface GoBackend {
  /** Replays seed+delta into a memory event log and mounts the handler. */
  boot(seedEvents: string, deltaEvents: string): void;
  /**
   * Serves one request through the http.Handler. onHeader fires at
   * WriteHeader time (so a streaming response is usable immediately), chunks
   * follow as the handler writes, and the promise resolves when the handler
   * returns — which for SSE is when the stream ends.
   */
  handle(
    method: string,
    url: string,
    headers: [string, string][],
    body: Uint8Array | null,
    onHeader: (status: number, headers: [string, string][]) => void,
    onChunk: (chunk: Uint8Array) => void
  ): Promise<void>;
  /** JSON array of every envelope appended after the seed head. */
  exportDelta(): string;
}

declare global {
  var __intraktible: GoBackend | undefined;
  var Go: new () => {
    importObject: WebAssembly.Imports;
    run(i: WebAssembly.Instance): Promise<void>;
  };
}

const post = (m: WorkerResponse, transfer: Transferable[] = []) =>
  (self as unknown as Worker).postMessage(m, transfer);

// fetchWithProgress fetches the wasm binary, reporting download progress to the
// page (the binary is ~10 MB, so the splash needs something to show). The body
// is re-wrapped so instantiateStreaming still compiles as bytes arrive.
async function fetchWithProgress(url: string): Promise<Response> {
  const res = await fetch(url);
  if (!res.ok || !res.body) throw new Error(`wasm binary: HTTP ${res.status}`);
  const total = Number(res.headers.get('content-length') ?? 0);
  let loaded = 0;
  const counted = res.body.pipeThrough(
    new TransformStream<Uint8Array, Uint8Array>({
      transform(chunk, controller) {
        loaded += chunk.byteLength;
        post({ kind: 'boot-progress', loaded, total });
        controller.enqueue(chunk);
      }
    })
  );
  return new Response(counted, { status: res.status, headers: res.headers });
}

async function boot(
  wasmURL: string,
  wasmExecURL: string,
  seedURL: string,
  delta: string
): Promise<void> {
  // The backend's "js" AI provider resolves globalThis.__intraktible_ai through
  // syscall/js, and the Go runtime executes HERE — so the hook must live on the
  // worker's global scope, registered before the first completion is requested.
  registerSimulatedAI();
  // wasm_exec.js is a plain script (no exports) that registers globalThis.Go on
  // evaluation. This is a module worker, so importScripts is unavailable — the
  // runtime asset is loaded by URL, resolved by the page against the app base.
  await import(/* @vite-ignore */ wasmExecURL);
  const go = new Go();
  const result = await WebAssembly.instantiateStreaming(
    await fetchWithProgress(wasmURL),
    go.importObject
  );
  // main() registers __intraktible then blocks on a never-closed channel.
  void go.run(result.instance);
  // Registration is synchronous inside main(); poll one microtask to be safe.
  await Promise.resolve();
  const backend = globalThis.__intraktible;
  if (!backend) throw new Error('the wasm backend did not register __intraktible');
  const seedRes = await fetch(seedURL);
  if (!seedRes.ok) throw new Error(`seed event log: HTTP ${seedRes.status}`);
  backend.boot(await seedRes.text(), delta);
}

self.onmessage = async (e: MessageEvent<WorkerRequest>) => {
  const msg = e.data;
  if (msg.kind === 'boot') {
    try {
      await boot(msg.wasmURL, msg.wasmExecURL, msg.seedURL, msg.delta);
      post({ kind: 'ready' });
    } catch (err) {
      post({ kind: 'boot-error', message: err instanceof Error ? err.message : String(err) });
    }
    return;
  }
  const backend = globalThis.__intraktible;
  if (!backend) throw new Error('request before boot');
  if (msg.kind === 'export-delta') {
    post({ kind: 'delta', events: backend.exportDelta() });
    return;
  }
  try {
    await backend.handle(
      msg.method,
      msg.url,
      msg.headers,
      msg.body,
      (status, headers) => post({ kind: 'response', id: msg.id, status, headers }),
      (chunk) => post({ kind: 'chunk', id: msg.id, body: chunk }, [chunk.buffer])
    );
    // Mutations append events; snapshot the delta after every request so the
    // page can persist it (cheap: the exporter only walks past the seed head).
    // Posted BEFORE 'done' so the page stores the delta before the response
    // resolves — a write the app has seen acknowledged must survive an
    // immediate navigation/reload.
    post({ kind: 'delta', events: backend.exportDelta() });
    post({ kind: 'done', id: msg.id });
  } catch (err) {
    post({
      kind: 'fetch-error',
      id: msg.id,
      message: err instanceof Error ? err.message : String(err)
    });
  }
};
