# intraktible — Known Issues & Bugs

Tracked alongside `PLAN.md`; updated in the same PR at the end of every phase.
Format: `ID | severity | component | description | status`.

## Open (deferred / limitations after Phase 0)
- `D1 | low | eventlog | WAL holds all events in memory and re-reads the whole file on open; fine for MVP, revisit with segments/Badger | open`
- `D2 | med | store | projection store is in-memory only; projections rebuild from the log at boot. Durable SQLite/Postgres JSONB adapters not yet implemented | open`
- `D3 | med | projection | a live-apply error stops the consumer (surfaced via Err) but the HTTP server keeps running; no auto-restart/dead-letter yet | open`
- `D4 | low | schema | no JSON-Schema validation lib yet; dynamic payloads not validated against per-flow input_schema | open`
- `D5 | med | ai | only the Stub provider exists; Claude/OpenAI/Gemini/Ollama adapters not yet wired | open`
- `D6 | med | web | embedded UI is a hand-written placeholder; the SvelteKit scaffold (web/) is not yet built into platform/web/assets; Svelte Flow added in Phase 1 | open`
- `D7 | low | auth | sessions are in-memory with no login endpoint/expiry yet; only the seeded dev API key is usable end-to-end | open`
- `D8 | low | projection | rebuild does not Reset collections (store empty at boot); needed once durable stores land so re-runs are idempotent | open`

## Fixed
_None yet._
