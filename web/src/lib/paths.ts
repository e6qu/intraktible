// SPDX-License-Identifier: AGPL-3.0-or-later
// In-app link helper. The app is served at the origin root when embedded in the Go
// binary (base ''), and under a sub-path for the public GitHub Pages demo (base
// '/demo'). SvelteKit does not rewrite author-written hrefs, so internal links must
// carry the base — appHref centralises that so every <a href> and goto() is portable.
import { base } from '$app/paths';

// appHref prefixes an in-app route with the SvelteKit base path. Anything that is
// not an app route passes through unchanged: the backend API ('/v1/…'), the sibling
// docs site ('/docs/…', deployed next to the demo, not under it), and absolute or
// fragment URLs. Idempotent and safe to wrap around any href/goto argument.
export function appHref(path: string): string {
  if (!path.startsWith('/')) return path; // external URL, mailto:, #anchor, relative
  if (path === '/v1' || path.startsWith('/v1/')) return path; // backend API
  if (path === '/healthz') return path;
  if (path === '/docs' || path.startsWith('/docs/')) return path; // sibling docs site
  return base + path;
}
