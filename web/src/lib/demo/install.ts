// SPDX-License-Identifier: AGPL-3.0-or-later
// Installs the client-side demo backend by overriding window.fetch. The app's
// api.ts calls fetch('/v1/...') with the global fetch by default, so this single
// interception serves every API call from the in-memory store — no server, runs
// on GitHub Pages. Only loaded (via dynamic import) in the demo build; the normal
// bundle never references this module.

import { handleDemo, type DemoResponse } from './router';
import { USERS, setDemoUser, persist, resetDemo, state, nextId, type DemoUser } from './store';
import { agentReply } from './agent';

// DemoControl is the small surface the demo UI (DemoBanner) reads off window to
// drive the identity switcher + reset, without statically importing this module
// (which would pull demo code into the normal bundle). Set only in the demo build.
export interface DemoControl {
  users: DemoUser[];
  current(): string;
  setUser(actor: string): void;
  reset(): void;
}

declare global {
  interface Window {
    __demo?: DemoControl;
  }
}

let installed = false;

// installDemoBackend replaces window.fetch with a wrapper that routes /v1/* to the
// demo handler and delegates everything else to the original fetch. Idempotent.
export function installDemoBackend(): void {
  if (installed || typeof window === 'undefined') return;
  installed = true;
  const original = window.fetch.bind(window);

  // Expose the identity-switch control for the demo banner (read off window so the
  // always-compiled banner needn't statically import demo code).
  window.__demo = {
    users: USERS,
    current: () => state.identity.actor,
    setUser: (actor: string) => void setDemoUser(actor),
    reset: resetDemo
  };

  // First-time visitors land on the guided Evaluator tour rather than the dense
  // builder cockpit — it's the 10-second "what is this" on-ramp. Returning visitors
  // (who've picked a persona) keep their choice; the real app's default is unchanged
  // since this runs only in the demo build.
  try {
    if (!localStorage.getItem('intraktible-persona')) {
      localStorage.setItem('intraktible-persona', 'evaluator');
    }
  } catch {
    // ignore (storage unavailable) — falls back to the app default persona
  }

  window.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    try {
      const { method, url } = describe(input, init);
      const parsed = new URL(url, window.location.origin);
      if (!parsed.pathname.startsWith('/v1/')) {
        return original(input, init);
      }
      const body = await readBody(input, init);
      const verb = method.toUpperCase();
      const res = handleDemo(verb, parsed.pathname, parsed.searchParams, body);
      // Persist after any successful mutation so flow/case/etc. progress survives a
      // reload — the demo accumulates state instead of resetting every page view.
      if (verb !== 'GET' && res.status < 400) persist();
      return toResponse(res);
    } catch {
      // Never throw out of the wrapper — a thrown fetch would crash the page.
      return jsonResponse({}, 200);
    }
  };

  installStreamMock();
}

// installStreamMock makes the agent "Stream a run" feature work in the static demo.
// The fetch override doesn't cover SSE/WebSocket, so the streaming endpoints would
// otherwise dead-end ("stream failed"). These shims replay the same agentReply the
// non-streaming run produces, chunked, and record the run so it shows in the list.
type AgentSchema = { properties?: Record<string, unknown> };
function streamSchema(name: string): AgentSchema | undefined {
  return state.agents.find((a) => a.name === name)?.schema as AgentSchema | undefined;
}
function chunksOf(text: string): string[] {
  const words = text.split(' ');
  const size = Math.max(1, Math.ceil(words.length / 4));
  const out: string[] = [];
  for (let i = 0; i < words.length; i += size) out.push(words.slice(i, i + size).join(' ') + ' ');
  return out;
}
function recordStreamRun(name: string, prompt: string, text: string): void {
  state.agentRuns.unshift({
    run_id: nextId('run'),
    agent: name,
    model: state.agents.find((a) => a.name === name)?.model,
    prompt,
    status: 'completed',
    text,
    at: new Date().toISOString()
  });
  const agent = state.agents.find((a) => a.name === name);
  if (agent) agent.runs += 1;
  persist();
}

function installStreamMock(): void {
  const STREAM = /^\/v1\/agents\/([^/]+)\/run\/stream$/;
  const WS = /^\/v1\/agents\/([^/]+)\/run\/ws$/;
  const RealES = window.EventSource;
  const RealWS = window.WebSocket;

  class DemoEventSource extends EventTarget {
    onerror: ((e: Event) => void) | null = null;
    private timers: ReturnType<typeof setTimeout>[] = [];
    constructor(url: string) {
      super();
      const u = new URL(url, window.location.origin);
      const mm = u.pathname.match(STREAM);
      if (!mm) return new RealES(url) as unknown as DemoEventSource;
      const name = decodeURIComponent(mm[1]);
      const prompt = u.searchParams.get('prompt') ?? '';
      const reply = agentReply(prompt, streamSchema(name));
      const pieces = chunksOf(reply.text);
      pieces.forEach((p, i) =>
        this.timers.push(
          setTimeout(
            () =>
              this.dispatchEvent(new MessageEvent('chunk', { data: JSON.stringify({ text: p }) })),
            40 * (i + 1)
          )
        )
      );
      this.timers.push(
        setTimeout(
          () => {
            recordStreamRun(name, prompt, reply.text);
            this.dispatchEvent(new Event('done'));
          },
          40 * (pieces.length + 1)
        )
      );
    }
    close(): void {
      this.timers.forEach(clearTimeout);
    }
  }

  class DemoWebSocket {
    onopen: ((e: Event) => void) | null = null;
    onmessage: ((e: MessageEvent) => void) | null = null;
    onerror: ((e: Event) => void) | null = null;
    onclose: ((e: Event) => void) | null = null;
    private name = '';
    private timers: ReturnType<typeof setTimeout>[] = [];
    constructor(url: string) {
      const u = new URL(url, window.location.origin);
      const mm = u.pathname.match(WS);
      if (!mm) return new RealWS(url) as unknown as DemoWebSocket;
      this.name = decodeURIComponent(mm[1]);
      setTimeout(() => this.onopen?.(new Event('open')), 0);
    }
    send(data: string): void {
      let prompt = '';
      try {
        prompt = String((JSON.parse(data) as { prompt?: unknown }).prompt ?? '');
      } catch {
        prompt = '';
      }
      const reply = agentReply(prompt, streamSchema(this.name));
      const pieces = chunksOf(reply.text);
      pieces.forEach((p, i) =>
        this.timers.push(
          setTimeout(
            () =>
              this.onmessage?.(
                new MessageEvent('message', { data: JSON.stringify({ type: 'chunk', text: p }) })
              ),
            40 * (i + 1)
          )
        )
      );
      this.timers.push(
        setTimeout(
          () => {
            recordStreamRun(this.name, prompt, reply.text);
            this.onmessage?.(
              new MessageEvent('message', { data: JSON.stringify({ type: 'done' }) })
            );
          },
          40 * (pieces.length + 1)
        )
      );
    }
    close(): void {
      this.timers.forEach(clearTimeout);
    }
  }

  window.EventSource = DemoEventSource as unknown as typeof EventSource;
  window.WebSocket = DemoWebSocket as unknown as typeof WebSocket;
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
