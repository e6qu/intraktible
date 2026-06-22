// SPDX-License-Identifier: AGPL-3.0-or-later
// Installs the client-side demo backend by overriding window.fetch. The app's
// api.ts calls fetch('/v1/...') with the global fetch by default, so this single
// interception serves every API call from the in-memory store — no server, runs
// on GitHub Pages. Only loaded (via dynamic import) in the demo build; the normal
// bundle never references this module.

import { handleDemo, type DemoResponse } from './router';

let installed = false;

// installDemoBackend replaces window.fetch with a wrapper that routes /v1/* to the
// demo handler and delegates everything else to the original fetch. Idempotent.
export function installDemoBackend(): void {
  if (installed || typeof window === 'undefined') return;
  installed = true;
  const original = window.fetch.bind(window);

  window.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    try {
      const { method, url } = describe(input, init);
      const parsed = new URL(url, window.location.origin);
      if (!parsed.pathname.startsWith('/v1/')) {
        return original(input, init);
      }
      const body = await readBody(input, init);
      const res = handleDemo(method.toUpperCase(), parsed.pathname, parsed.searchParams, body);
      return toResponse(res);
    } catch {
      // Never throw out of the wrapper — a thrown fetch would crash the page.
      return jsonResponse({}, 200);
    }
  };
}

// describe extracts method + url from the polymorphic fetch input.
function describe(input: RequestInfo | URL, init?: RequestInit): { method: string; url: string } {
  if (typeof input === 'string') return { method: init?.method ?? 'GET', url: input };
  if (input instanceof URL) return { method: init?.method ?? 'GET', url: input.toString() };
  // Request
  return { method: init?.method ?? input.method ?? 'GET', url: input.url };
}

// readBody parses a JSON request body when present (best-effort; empty object on
// any absence or parse failure so handlers can read fields unconditionally).
async function readBody(
  input: RequestInfo | URL,
  init?: RequestInit
): Promise<Record<string, unknown>> {
  let raw: string | undefined;
  if (init?.body && typeof init.body === 'string') {
    raw = init.body;
  } else if (input instanceof Request) {
    try {
      raw = await input.clone().text();
    } catch {
      raw = undefined;
    }
  }
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw);
    return typeof parsed === 'object' && parsed !== null ? (parsed as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

function toResponse(res: DemoResponse): Response {
  if (res.text !== undefined) {
    return new Response(res.text, {
      status: res.status,
      headers: { 'content-type': 'text/plain' }
    });
  }
  return jsonResponse(res.body, res.status);
}

function jsonResponse(body: unknown, status: number): Response {
  return new Response(JSON.stringify(body ?? {}), {
    status,
    headers: { 'content-type': 'application/json' }
  });
}
