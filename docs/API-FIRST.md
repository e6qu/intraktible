<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# API-first guarantee

The web UI is **one client of the public API, not a privileged insider**. Every data
read and every mutation it performs goes through a documented `/v1` endpoint — there
are no UI-only backdoors, no server-side data path that bypasses the API, and no
private endpoints. This is what lets personas (see [persona.ts](../web/src/lib/persona.ts))
and external / embedded UIs be flexible adaptations over the same contract rather
than forks, and it underpins the external-API compatibility surface.

## The invariants

1. **All network calls go through `web/src/lib/api.ts`.** Every endpoint is reached
   via the module's injectable `fetcher` (defaulting to `fetch`); no component, store,
   or route issues a raw `fetch(...)` of its own. `api.ts` is the single, typed,
   documented seam to the backend.
2. **Every endpoint is under `/v1`.** The only non-`/v1` URLs the UI references are
   links (not data calls): the generated API reference at `/docs` and a flow's
   contract at `/v1/flows/{slug}/openapi.json`.
3. **No SvelteKit server routes.** There are no `+page.server.ts`, `+layout.server.ts`,
   or `+server.ts` files. The app is a static client; all data loading is client-side
   against `/v1`, so the deployed UI and any third-party client see the exact same API.
4. **The only UI-only state is local preference** — the chosen persona and the
   light/dark theme, both in `localStorage`. Neither is server data; everything else
   (flows, decisions, policies, cases, agents, keys, …) is a `/v1` call.

## Enforcement

These invariants are not just documented — `web/src/lib/api-first.test.ts` asserts
them in CI: no raw `fetch(` outside `api.ts`, no server-route files, and every inline
endpoint string in `api.ts` beginning with `/v1`. A change that adds a UI-only data
path fails the test.
