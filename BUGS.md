# intraktible — Known Issues & Bugs

Tracked alongside `PLAN.md`; updated in the same PR at the end of every phase.
Format: `ID | severity | component | description | status`.

## Open (deferred / limitations after Phase 0)
- `D1 | low | eventlog | WAL holds all events in memory and re-reads the whole file on open; fine for MVP, revisit with segments/Badger | open`
- `D3 | med | projection | a live-apply error stops the consumer (surfaced via Err) but the HTTP server keeps running; no auto-restart/dead-letter yet | open`
- `D5 | med | ai | only the Stub provider exists; Claude/OpenAI/Gemini/Ollama adapters not yet wired | open`

## Open (deferred / limitations after Phase 1)
- `D9 | low | decision-engine | CEL conditions not implemented; expr-lang serves Rule/Split conditions + Assignment and Starlark serves the Code node, so conditions work — CEL is an optional alternative engine | deferred`
- `D10 | low | web | builder UI lacks drag-to-connect on the canvas and bespoke per-node config panels (raw JSON config textarea for now) | deferred`
- `D11 | low | decision-engine | each decide appends one event per node (started + N node-evaluated + completed/failed); fine for MVP, could batch for high-volume flows | deferred`

## Open (deferred / limitations after Phase 2)
- `D12 | low | case-manager | SLA days-left and SLA state are computed at read time from created_at + sla_days against the wall clock; the stored projection stays clock-free (replay-stable). No SLA-breach events/alerts are emitted — overdue is derived on read, not pushed | deferred`
- `D13 | low | web | case detail shows the raw context JSON inline; no schema-aware/rich context view (e.g. rendering the source decision's inputs/outputs) yet | deferred`

## Open (deferred / limitations during Phase 3)
- `D14 | low | context-layer | reference connectors cover http + a deterministic mock_bureau; a SQL connector is not implemented (needs a driver/DB). The Connect interface + registry make it pluggable when a backend lands | deferred`
- `D15 | low | context-layer | the http connector fetches an operator-configured URL (the intended Custom Connect feature); it validates the scheme + bounds time/size but has no allow-list/SSRF policy — add egress controls before exposing it to untrusted config | open`

## Open (deferred / limitations after Phase 4)
- `D16 | low | agent-manager | an agent's tool set is declared and stored but tools are not executed yet (no tool-calling loop); the Stub provider ignores them. Real tool-calling lands with a non-Stub provider | deferred`
- `D17 | low | agent-manager | runs are synchronous (call the provider, record the result); no async/queued runs, streaming, or in-flight status. A structured-output schema is passed to the provider but the response is not validated against it (the Stub returns {}) | deferred`

## Open (deferred / limitations during Phase 5)
- `D19 | low | decision-engine | decide input is validated against a supported subset of JSON Schema (object type, required, per-property type incl. integer/number/boolean/array/object/null); nested schemas, $ref, enum, format, allOf/anyOf etc. are accepted but not enforced. Swap in a full validator if richer contracts are needed | open`
- `D21 | low | store | only the SQLite durable adapter exists (plus in-memory); a Postgres store.Store adapter (pgx) is not implemented yet — useful for large/shared projections. On a restart the SQLite store is still fully rebuilt from the log rather than resumed incrementally from Head (correct but not optimized) | open`
- `D20 | low | auth | sessions are still in-memory (lost on restart) and the builder UI still sends X-Api-Key per request rather than using the new POST /v1/login cookie flow; durable session storage + UI adoption (a login page) are follow-ups | open`
- `D18 | med | eventlog | the file WAL is single-process (each process holds its own in-memory copy + appends locally). The split-services compose profile therefore gives each module an independent log; full cross-component split (escalation, Rule/Connect/AI nodes reading another layer) needs a shared/networked log backend (Badger/Postgres/gRPC) behind the existing Log interface. The monolith profile is unaffected | open`

## Fixed
- `D2 | store | added a durable SQLite projection store (store.NewSQLite, pure-Go modernc.org/sqlite — no CGO) behind the existing store.Store interface, selectable with serve --store=sqlite (persists to <data-dir>/projections.db, WAL + busy_timeout for one writer / many readers). Verified data survives a restart. Postgres adapter split out as D21. | fixed`
- `D7 | auth | added a login flow: POST /v1/login exchanges a valid API key for an HttpOnly session cookie (the Authenticate middleware already accepted it), POST /v1/logout revokes it, GET /v1/me returns the caller; sessions now expire (DefaultSessionTTL) and can be revoked. Remaining follow-ups split out as D20. | fixed`
- `D8 | projection | rebuild is now idempotent: the Projector interface gained Collections(), and RebuildTo resets each projector's collections before replaying — so rebuilding into a non-empty store (a durable store, or a repeated replay) no longer double-applies. Verified by replaying the same store twice (counts unchanged). | fixed`
- `D4 | decision-engine | decide input is now validated against the version's input_schema before the run is recorded — a contract violation is a 400, not a recorded decision. Pure domain.ValidateInput enforces a JSON-Schema subset (object type / required / per-property type) with no new dependency; the unenforced keywords are tracked as D19. | fixed`
- `D6 | web | the production SvelteKit build now embeds and serves correctly: web.Handler does SPA fallback (a real embedded file is served as-is; any other path returns index.html 200) so client-side routes like /engine and /cases/{id} work from the binary. make dist (web + build) and the Dockerfile produce the single self-contained artifact; a fresh checkout still embeds the committed placeholder so go build always works, and the build output is gitignored. | fixed`
