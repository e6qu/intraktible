// SPDX-License-Identifier: AGPL-3.0-or-later
// Static SPA: prerender the shell, render client-side (talks to the API).
import { browser } from '$app/environment';

export const prerender = true;
export const ssr = false;

// In the public GitHub Pages demo (build:demo sets VITE_DEMO=true) and only in the
// browser, install the client-side mock backend before any component mounts so the
// app's /v1 API requests are served from the in-memory store. The static
// import.meta.env.VITE_DEMO is replaced with `false` in the normal build, so this
// branch — and the dynamically imported demo code — is dead-code-eliminated from
// the embedded production bundle.
export async function load() {
  if (import.meta.env.VITE_DEMO && browser) {
    const { installDemoBackend } = await import('$lib/demo/install');
    installDemoBackend();
  }
  return {};
}
