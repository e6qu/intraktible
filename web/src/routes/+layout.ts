// SPDX-License-Identifier: AGPL-3.0-or-later
// Static SPA: prerender the shell, render client-side (talks to the API).
import { browser } from '$app/environment';

export const prerender = true;
export const ssr = false;

// In the public GitHub Pages demo (build:demo sets VITE_DEMO=true) and only in the
// browser, boot the REAL Go backend (compiled to wasm, hosted in a worker) before
// any component mounts, so the app's /v1 API requests are served by the exact
// handler the native binary mounts. The static import.meta.env.VITE_DEMO is
// replaced with `false` in the normal build, so this branch — and the dynamically
// imported demo shell — is dead-code-eliminated from the embedded production bundle.
export async function load() {
  if (import.meta.env.VITE_DEMO && browser) {
    const { startEmbeddedDemo } = await import('$lib/backend/demo');
    await startEmbeddedDemo();
  }
  return {};
}
