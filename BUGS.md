# intraktible — Known Issues & Bugs

Tracked alongside `PLAN.md`; updated in the same PR at the end of every phase.
Format: `ID | severity | component | description | status`.

## Open (deferred / limitations after Phase 0)
- `D1 | low | eventlog | WAL holds all events in memory and re-reads the whole file on open; fine for MVP, revisit with segments/Badger | open`
- `D2 | med | store | projection store is in-memory only; projections rebuild from the log at boot. Durable SQLite/Postgres JSONB adapters not yet implemented | open`
- `D3 | med | projection | a live-apply error stops the consumer (surfaced via Err) but the HTTP server keeps running; no auto-restart/dead-letter yet | open`
- `D4 | low | schema | no JSON-Schema validation lib yet; decide input is not validated against the per-flow input_schema (stored opaquely on each version) | open`
- `D5 | med | ai | only the Stub provider exists; Claude/OpenAI/Gemini/Ollama adapters not yet wired | open`
- `D6 | med | web | the Svelte Flow builder UI is built (web/src/routes/engine) and runs via `make dev` + Playwright, but the production SvelteKit build is not auto-embedded into platform/web/assets — the binary serves a hand-written placeholder until `make web` runs | open`
- `D7 | low | auth | sessions are in-memory with no login endpoint/expiry yet; only the seeded dev API key is usable end-to-end | open`
- `D8 | low | projection | rebuild does not Reset collections (store empty at boot); needed once durable stores land so re-runs are idempotent | open`

## Open (deferred / limitations after Phase 1)
- `D9 | low | decision-engine | CEL conditions not implemented; expr-lang serves Rule/Split conditions + Assignment and Starlark serves the Code node, so conditions work — CEL is an optional alternative engine | deferred`
- `D10 | low | web | builder UI lacks drag-to-connect on the canvas and bespoke per-node config panels (raw JSON config textarea for now) | deferred`
- `D11 | low | decision-engine | each decide appends one event per node (started + N node-evaluated + completed/failed); fine for MVP, could batch for high-volume flows | deferred`

## Fixed
_None yet._
