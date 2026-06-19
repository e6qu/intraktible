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
  `production`/`*`) is **enforced** on the decide endpoints. Do **not** ship the dev seed
  key (`--dev-api-key`, disable with `--dev-api-key=""`).
- **Connector egress** — SSRF-guarded at dial time by default; `INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE`
  opens private/loopback targets (logged loudly) — leave **off** unless connectors must reach
  internal hosts.
- **Connector secrets at rest** — `INTRAKTIBLE_CONNECTOR_SECRET_KEY` (+ `…_KEYS_PREVIOUS` for
  rotation) or an external KMS via `INTRAKTIBLE_KMS_PROVIDER` (AWS / GCP).
- **SQL connector files** — `ITK_SQL_CONNECTOR_DIR` confines sqlite-connector databases to a
  directory (always read-only).
- **PII erasure** — configure erasure fields so recorded decision PII is crypto-shreddable.
- **Request bodies** are capped at 8 MiB (JSON endpoints); the large-job path is
  `POST …/decide/stream` (NDJSON, unbounded, streamed).
- **AI provider** — `INTRAKTIBLE_AI_BASE_URL` / `_API_KEY` / `_MODEL` enable a real LLM for
  AI nodes and the copilot; the Stub is the offline fallback.

## Health & introspection

- `GET /healthz` — liveness + projection health (503 `degraded` if a projection stalled, so an
  orchestrator can depool/restart).
- `GET /version` — build revision + Go toolchain (confirm what's deployed).
- `GET /openapi.json` + `GET /docs` — the served API contract; `GET /v1/flows/{slug}/openapi.json`
  is a per-flow contract for integrators.

## Observability

- Decisions are event-sourced and replayable; `GET /v1/decisions` (filter by flow/env/status/time,
  `include_node_results=false` for lighter pages).
- Flow monitors (`distribution_drift` etc.) + the immutable audit log (`GET /v1/audit`).
- **Model drift** — `GET /v1/models/{name}/drift` (PSI vs a captured baseline; `?window=Nd`),
  with a `POST …/monitor` PSI threshold.
- Optional monitor scheduler: `INTRAKTIBLE_MONITOR_INTERVAL` pushes firing monitors to webhooks.

## Quality gate (CI parity)

`make check` runs the full gate locally — go (gofmt, vet, golangci-lint, gosec, dupl, deadcode,
govulncheck, AGPL licenses, `-race` tests) and web (prettier, eslint+security, svelte-check,
vitest, Playwright + an embedded-binary smoke). Pre-commit + pre-push hooks run the same targets.
