// SPDX-License-Identifier: AGPL-3.0-or-later
// The demo shell: boots the REAL Go backend (compiled to wasm, hosted in a
// worker) and wires the page to it. Loaded only in the VITE_DEMO build, and
// deliberately thin — transport (fetch/EventSource/WebSocket routed through the
// bridge) plus seed identity (the roster's per-user API keys, minted at boot
// through the same keys API an admin would call). The application itself is
// byte-for-byte the production bundle.
//
// Boot is all-or-nothing: a splash covers the page while the ~10 MB engine
// downloads and replays the seed, and any failure replaces it with a loud
// full-page error — there is no degraded or mocked mode to fall back to.

import { base } from '$app/paths';
import { bootEmbeddedBackend, bridgeFetch, clearEmbeddedDelta, hasEmbeddedDelta } from './bridge';
import { installEmbeddedStreams } from './streams';
import { createApiKey, login, type Role, type Scope } from '$lib/api';

// The dev admin key the wasm server is assembled with (cmd/intraktible-wasm) —
// used once per boot to mint the roster's session keys, exactly like a local
// `intraktible serve` sandbox.
const DEV_KEY = 'dev-sandbox-key';

// sessionStorage: the minted actor→key map (secrets are shown once by the API,
// so the switcher keeps them for the session) and the active actor, which
// survives a reload so a mid-flow refresh doesn't revert the visitor's role.
const KEYS_KEY = 'intraktible-demo-keys';
const ACTOR_KEY = 'intraktible-demo-actor';

/** One roster entry from web/static/demo-users.json (no secrets — keys are minted at boot). */
export interface DemoUser {
  actor: string;
  name: string;
  role: string;
  title: string;
  scope: string;
}

// DemoControl is the small surface the demo UI (DemoBanner) reads off window to
// drive the identity switcher + reset, without statically importing this module
// (which would pull demo wiring into the normal bundle). Set only in the demo build.
export interface DemoControl {
  users: DemoUser[];
  current(): string;
  /** Switches the acting identity by logging in with that user's minted key. */
  setUser(actor: string): Promise<void>;
  /** Clears the visitor's delta + minted keys; the banner reloads to reboot from the seed. */
  reset(): void;
}

declare global {
  interface Window {
    __demo?: DemoControl;
  }
}

let started = false;

/**
 * Boots the embedded backend and installs the transport. Resolves when the app
 * may render (backend serving, demo cast signed in); rejects — leaving a loud
 * full-page error — when the engine cannot start.
 */
export async function startEmbeddedDemo(): Promise<void> {
  if (started || typeof window === 'undefined') return;
  started = true;
  const ui = mountSplash();
  try {
    // The retired TypeScript-mock blob (pre-wasm builds) — clear it so it never
    // lingers in visitors' storage; the event-log delta replaced it wholesale.
    localStorage.removeItem('intraktible-demo-state');
    // First-time visitors land on the guided Evaluator tour rather than the
    // dense builder cockpit; returning visitors keep their choice.
    if (!localStorage.getItem('intraktible-persona')) {
      localStorage.setItem('intraktible-persona', 'evaluator');
    }

    ui.status('Downloading the decision engine…');
    const assets = {
      wasmURL: `${base}/intraktible.wasm`,
      wasmExecURL: `${base}/wasm_exec.js`,
      seedURL: `${base}/demo-seed.json`
    };
    try {
      await bootEmbeddedBackend(assets, ui.progress);
    } catch (bootErr) {
      // A saved session (the event-log delta in localStorage) from an earlier demo build
      // can be incompatible with the current seed/engine and abort the boot. It is
      // disposable — drop it and boot the clean seed once more, so a returning visitor is
      // not stuck on a dead demo. A boot failure with no delta is a real error: re-throw.
      if (!hasEmbeddedDelta()) throw bootErr;
      console.warn(
        'intraktible demo: discarding an incompatible saved session and restarting',
        bootErr
      );
      clearEmbeddedDelta();
      ui.status('Restoring the demo…');
      await bootEmbeddedBackend(assets, ui.progress);
    }

    ui.status('Signing in the demo team…');
    const roster = await loadRoster();
    const keys = await mintKeys(roster);
    sessionStorage.setItem(KEYS_KEY, JSON.stringify([...keys]));
    const stored = sessionStorage.getItem(ACTOR_KEY);
    let active = stored && roster.some((u) => u.actor === stored) ? stored : roster[0].actor;
    await login(keyOf(keys, active), bridgeFetch);
    sessionStorage.setItem(ACTOR_KEY, active);

    installBridgedFetch();
    installEmbeddedStreams();

    window.__demo = {
      users: roster,
      current: () => active,
      async setUser(actor: string): Promise<void> {
        await login(keyOf(keys, actor), bridgeFetch);
        active = actor;
        sessionStorage.setItem(ACTOR_KEY, actor);
      },
      reset(): void {
        clearEmbeddedDelta();
        sessionStorage.removeItem(KEYS_KEY);
        sessionStorage.removeItem(ACTOR_KEY);
      }
    };

    ui.remove();
  } catch (err) {
    ui.fail(err instanceof Error ? err.message : String(err));
    throw err;
  }
}

// loadRoster fetches the demo cast (a static asset, not an API route).
async function loadRoster(): Promise<DemoUser[]> {
  const res = await fetch(`${base}/demo-users.json`);
  if (!res.ok) throw new Error(`demo roster: HTTP ${res.status}`);
  const roster = (await res.json()) as DemoUser[];
  if (!Array.isArray(roster) || roster.length === 0) throw new Error('demo roster is empty');
  return roster;
}

// mintKeys creates one API key per roster user through the real keys API.
// Managed keys are operational state (their hashes never enter the event log),
// so a replayed boot cannot resolve earlier secrets — every boot mints afresh.
async function mintKeys(roster: DemoUser[]): Promise<Map<string, string>> {
  const keys = new Map<string, string>();
  for (const u of roster) {
    const { secret } = await createApiKey(
      DEV_KEY,
      {
        name: `${u.name} — ${u.title}`,
        actor: u.actor,
        role: u.role as Role,
        scope: u.scope as Scope
      },
      bridgeFetch
    );
    keys.set(u.actor, secret);
  }
  return keys;
}

function keyOf(keys: Map<string, string>, actor: string): string {
  const key = keys.get(actor);
  if (!key) throw new Error(`no minted key for demo user ${actor}`);
  return key;
}

// installBridgedFetch routes the backend's routes (/v1/*, /healthz) through the
// worker and leaves every other request (assets, docs, external) untouched.
function installBridgedFetch(): void {
  const original = window.fetch.bind(window);
  window.fetch = (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const raw = input instanceof Request ? input.url : String(input);
    const url = new URL(raw, window.location.origin);
    const embedded =
      url.origin === window.location.origin &&
      (url.pathname === '/healthz' || url.pathname === '/v1' || url.pathname.startsWith('/v1/'));
    return embedded ? bridgeFetch(input, init) : original(input, init);
  };
}

// --- boot splash / error screen ---------------------------------------------------
// Rendered with plain DOM: it exists BEFORE the app (and its stylesheet) mounts.

interface BootUI {
  status(text: string): void;
  progress(loaded: number, total: number): void;
  fail(message: string): void;
  remove(): void;
}

const BOOT_CSS = `
  #demo-boot { position: fixed; inset: 0; z-index: 9999; display: grid; place-items: center;
    background: #10131a; color: #e6e8ee; font-family: system-ui, sans-serif; text-align: center; }
  #demo-boot .brand { font-size: 1.4rem; font-weight: 700; letter-spacing: -0.02em; margin-bottom: 1rem; }
  #demo-boot .msg { color: #9aa3b2; font-size: 0.95rem; margin-bottom: 1rem; }
  #demo-boot .bar { width: 260px; height: 6px; margin: 0 auto; border-radius: 999px; background: #262c38; overflow: hidden; }
  #demo-boot .fill { width: 0%; height: 100%; border-radius: 999px; background: #3b5bdb; transition: width 0.15s ease; }
  #demo-boot .err { max-width: 34rem; padding: 0 1.5rem; }
  #demo-boot .err h1 { color: #f87171; font-size: 1.2rem; }
  #demo-boot .err pre { text-align: left; white-space: pre-wrap; word-break: break-word;
    background: #1a1f29; border: 1px solid #343c4c; border-radius: 8px; padding: 0.8rem; font-size: 0.8rem; }
  #demo-boot .err button { font: inherit; padding: 0.45rem 1rem; border-radius: 8px; cursor: pointer;
    border: 1px solid #4c5566; background: #262c38; color: #e6e8ee; }`;

function div(className: string): HTMLDivElement {
  const d = document.createElement('div');
  d.className = className;
  return d;
}

function mountSplash(): BootUI {
  const el = document.createElement('div');
  el.id = 'demo-boot';
  const style = document.createElement('style');
  style.textContent = BOOT_CSS;
  const brand = div('brand');
  brand.textContent = 'intraktible';
  const msg = div('msg');
  msg.textContent = 'Starting the decision engine in your browser…';
  const fill = div('fill');
  const bar = div('bar');
  bar.appendChild(fill);
  const box = div('box');
  box.append(brand, msg, bar);
  el.append(style, box);
  document.body.appendChild(el);
  return {
    status(text) {
      msg.textContent = text;
    },
    progress(loaded, total) {
      if (total > 0) fill.style.width = `${Math.min(100, Math.round((loaded / total) * 100))}%`;
      const mb = (loaded / (1 << 20)).toFixed(1);
      msg.textContent = `Downloading the decision engine… ${mb} MB`;
    },
    fail(message) {
      box.remove();
      const err = div('err');
      const h = document.createElement('h1');
      h.textContent = 'The demo failed to start';
      const pre = document.createElement('pre');
      pre.textContent = message;
      const btn = document.createElement('button');
      btn.textContent = 'Reset the demo and reload';
      btn.onclick = () => {
        clearEmbeddedDelta();
        sessionStorage.removeItem(KEYS_KEY);
        sessionStorage.removeItem(ACTOR_KEY);
        location.reload();
      };
      err.append(h, pre, btn);
      el.appendChild(err);
    },
    remove() {
      el.remove();
    }
  };
}
