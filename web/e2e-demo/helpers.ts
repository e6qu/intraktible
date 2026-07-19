// SPDX-License-Identifier: AGPL-3.0-or-later
// Shared helpers for the demo (wasm-backed) e2e suite. The demo boots the REAL Go
// backend compiled to wasm in a worker and replays a seed event log; there is no
// HTTP server, so these helpers wrap the boot/readiness, identity, and persona
// mechanics every spec needs. See playwright.demo.config.ts for why navigations are
// expensive (a ~10 MB engine + seed replay per full load).
import { expect, type Page } from '@playwright/test';

// The demo roster (static/demo-users.json), by role. Switching to one of these is a
// real /v1/login as that user's minted key, so RBAC is exercised end-to-end.
export const DEMO_USERS = {
  admin: { actor: 'ava.chen', label: 'Ava Chen · admin' },
  approver: { actor: 'marcus.reed', label: 'Marcus Reed · approver' },
  editor: { actor: 'priya.nair', label: 'Priya Nair · editor' },
  operator: { actor: 'diego.santos', label: 'Diego Santos · operator' },
  viewer: { actor: 'lena.hoff', label: 'Lena Hoff · viewer' }
} as const;

export type DemoRole = keyof typeof DEMO_USERS;

function demoUser(role: DemoRole): (typeof DEMO_USERS)[DemoRole] {
  switch (role) {
    case 'admin':
      return DEMO_USERS.admin;
    case 'approver':
      return DEMO_USERS.approver;
    case 'editor':
      return DEMO_USERS.editor;
    case 'operator':
      return DEMO_USERS.operator;
    case 'viewer':
      return DEMO_USERS.viewer;
  }
}

type DemoBridge = {
  current(): string;
  setUser(actor: string): Promise<void>;
  users: { actor: string; role: string }[];
};

// trackPageErrors collects uncaught exceptions — the failure mode the smoke exists to
// catch (the backend returned a shape a page couldn't consume). Assert `.toEqual([])`.
export function trackPageErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));
  return errors;
}

// forcePersona pins the persona before the first paint. It MUST be called before the
// first goto: the no-flash boot reads localStorage on the first frame, and the demo
// otherwise defaults a first-time visitor to the guided 'evaluator' tour. Theme is
// pinned too so contrast/colour is deterministic.
export async function forcePersona(
  page: Page,
  persona: string,
  theme: 'light' | 'dark' = 'light'
): Promise<void> {
  await page.addInitScript(
    ([p, t]) => {
      localStorage.setItem('intraktible-persona', p);
      localStorage.setItem('intraktible-theme', t);
    },
    [persona, theme]
  );
}

// waitForBackend resolves once the wasm bridge is installed — required before any raw
// fetch()/__demo call, which would otherwise race onto the dead preview server's /v1.
export async function waitForBackend(page: Page): Promise<void> {
  await page.waitForFunction(() => '__demo' in window);
}

// gotoReady navigates (relative to the /intraktible/demo/ base — never a leading
// slash, which escapes the base) and waits for the signed-in shell to render past the
// boot splash. '' is the home dashboard.
export async function gotoReady(page: Page, route: string): Promise<void> {
  await page.goto(route);
  await expect(page.locator('h1, h2').first()).toBeVisible();
}

// openFlow reaches a flow's builder via its list row — seed ids are random per
// regeneration, so a detail page is never reached by a hardcoded id.
export async function openFlow(page: Page, slug: string): Promise<void> {
  await gotoReady(page, 'engine');
  const row = page.locator('tbody tr').filter({ hasText: slug });
  await row.first().locator('a[href*="/engine/"]').first().click();
  await expect(page.locator('h1, h2').first()).toBeVisible();
}

// switchRole drives the DemoBanner identity switcher and waits for the async
// /v1/login to commit (the select flips optimistically before the login lands), so a
// following assertion/reload sees the new role's gating. Returns the actor.
export async function switchRole(page: Page, role: DemoRole): Promise<string> {
  const { actor, label } = demoUser(role);
  await page.getByLabel('Demo user (switch acting identity)').selectOption({ label });
  await page.waitForFunction(
    (a) => (window as unknown as { __demo?: DemoBridge }).__demo?.current() === a,
    actor
  );
  return actor;
}

// api runs fn in the page with the wasm-bridged fetch available, after the bridge is
// installed. Use it to resolve seed ids/counts or drive mutations directly.
export async function api<T>(page: Page, fn: () => Promise<T>): Promise<T> {
  await waitForBackend(page);
  return page.evaluate(fn);
}
