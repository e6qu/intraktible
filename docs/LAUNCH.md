<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# Launch / operations checklist

A practical pre-launch and day-2 reference for running intraktible. The single binary
serves the API and the embedded UI; everything is configured by flags + environment.

## Durability (pick per environment)

| Concern | Dev default | Production |
| --- | --- | --- |
| Projection store | in-memory (rebuilt from the log at boot) | `--store=sqlite` (single node) or `--store=postgres` (`INTRAKTIBLE_POSTGRES_DSN`) for shared/large |
| Event log | file WAL | `--log=sqlite` (cross-process) — the system of record; back it up |
| Modules | `--modules=all` | split per service with a shared `--log`/`--store` volume |

A durable store **resumes from a checkpoint** at boot (no full rebuild); the in-memory
store always full-rebuilds from the log.

## Security toggles (set before exposing the API)

- **API keys** — issue scoped, role-bound keys via `POST /v1/api-keys`. Scope (`sandbox`/
  `production`/`*`) is **enforced** on the decide endpoints, and is preserved across the
  API-key→session login exchange (a session cannot widen a scoped key). The dev seed key
  (`--dev-api-key`) is seeded **only with the in-memory store**, so a durable (production)
  deployment never boots with it regardless of the flag; issue managed keys instead.
- **Connector egress** — SSRF-guarded at dial time by default; `INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE`
  opens private/loopback targets (logged loudly) — leave **off** unless connectors must reach
  internal hosts.
- **Connector secrets at rest** — `INTRAKTIBLE_CONNECTOR_SECRET_KEY` (+ `…_KEYS_PREVIOUS` for
  rotation) or an external KMS via `INTRAKTIBLE_KMS_PROVIDER` (AWS / GCP). Credential config
  fields (`token`/`secret`/`api_key`/`auth`/…) are sealed under this key and never served back
  unredacted. Connector config is validated at **define** time, so a bad endpoint/credential
  fails on save, not on the first decide.
- **Connector auth** — the `http`/`graphql` connectors take an `auth` block (`bearer` | `header` |
  `basic` | `query` | `oauth2`) plus custom `headers`; `oauth2` is the client-credentials grant
  (token fetched from `token_url`, cached by its expiry, sent as a bearer). `plaid` and `stripe`
  are first-class provider adapters (preconfigured base URL + auth scheme — supply only credentials
  + the request).
- **SQL connector files** — `ITK_SQL_CONNECTOR_DIR` confines sqlite-connector databases to a
  directory (always read-only).
- **PII erasure** — configure erasure fields so recorded decision PII is crypto-shreddable.
- **Request bodies** are capped at 8 MiB (JSON endpoints); the large-job path is
  `POST …/decide/stream` (NDJSON, unbounded, streamed).
- **AI provider** — `INTRAKTIBLE_AI_BASE_URL` / `_API_KEY` / `_MODEL` enable a real LLM for
  AI nodes and the copilot. Without one, AI operations fail loudly; the canned Stub is
  opt-in only (`INTRAKTIBLE_AI_STUB=1`, dev/tests) — never silently substituted.

## Health & introspection

- `GET /healthz` — liveness + projection health (503 `degraded` if a projection stalled, so an
  orchestrator can depool/restart).
- `GET /version` — build revision + Go toolchain (confirm what's deployed).
- `GET /metrics` — Prometheus exposition (unauthenticated, aggregate counters only): HTTP
  request rate/latency by route, projection freshness (`intraktible_projection_applied_seq`) +
  errors, scheduler ticks, Go runtime/process. Point a Prometheus scrape at it.
- `GET /openapi.json` + `GET /docs` — the served API contract; `GET /v1/flows/{slug}/openapi.json`
  is a per-flow contract for integrators.

## Observability

- Decisions are event-sourced and replayable; `GET /v1/decisions` (filter by flow/env/status/time,
  `include_node_results=false` for lighter pages).
- Flow monitors (`distribution_drift` etc.) + the immutable audit log (`GET /v1/audit`).
- **Model drift** — `GET /v1/models/{name}/drift` (PSI vs a captured baseline; `?window=Nd`),
  with a `POST …/monitor` PSI threshold.
- Optional schedulers: `INTRAKTIBLE_MONITOR_INTERVAL` (e.g. `1m`) sweeps on that cadence and
  pushes both firing **flow monitors** and **model-drift** crossings to webhooks — on the
  ok→firing edge only (deduped), resetting on firing→ok. `INTRAKTIBLE_MODEL_DRIFT_WINDOW` (days)
  narrows the drift window the scheduler fires on (default: all-time cumulative).

## Quality gate (CI parity)

`make ci` runs the full gate locally — go (gofmt, vet, golangci-lint, gosec, dupl, deadcode,
govulncheck, AGPL licenses, `-race` tests) and web (prettier, eslint+security, svelte-check,
vitest, Playwright + an embedded-binary smoke). `make check` is the fast subset (fmt, vet,
typecheck, test). Pre-commit + pre-push hooks run the same targets.
