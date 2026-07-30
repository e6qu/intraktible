<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# intraktible

Open-source MVPs of a commercial **Agentic Decision Platform**, in four components:

- **decision-engine/** — drag-and-drop builder + execution runtime for versioned decision flows
- **case-manager/** — human-review queues & dashboards for escalated decisions
- **context-layer/** — entities/events/features data model + connectors
- **agent-manager/** — configure/run/monitor LLM task-agents inside flows

The Decision Engine also includes governed, stable-cohort experiments with
decision-linked outcomes and statistical guardrails, plus durable multi-worker
population decision/backtest jobs with pause, recovery, retry, and retained results.

**Stack:** Go (functional core / imperative shell) backend · SvelteKit + Svelte Flow frontend ·
pure-Go embedded append-only **event log** with **hybrid event sourcing** + JSONB projections
(pluggable SQLite/Postgres) · modular monolith that also splits into services · pluggable AI provider.

See **[PLAN.md](PLAN.md)** for the full architecture and roadmap. Status: the **MVP and the first
enterprise platform tracks are complete** — all four components, shared governance, durable execution,
experimentation/outcomes, and population automation are built, with replay/rollback operator tooling
and split-service deployment profiles. Start at **[AGENTS.md](AGENTS.md)**; a runnable end-to-end
walkthrough is in **[docs/EXAMPLE.md](docs/EXAMPLE.md)**. The delivered audit is
**[BUGS.md](BUGS.md)**.

**Live demo:** the REAL Go backend, compiled to WebAssembly, runs entirely in the browser (no server;
a seeded event log replays at boot) at
**[e6qu.github.io/intraktible/demo/](https://e6qu.github.io/intraktible/demo/)** — explore every persona
without signing up. Built and deployed from `web/` by the `pages` workflow.

**AI-readable pages:** every page of the UI exports itself for an AI agent — **Copy for AI** in the
header (or the page guide's export row) produces a semi-structured markdown document: what the page
is, the exact REST calls it made this visit (the same `/v1` API serves self-hosted deployments;
OpenAPI 3.1 at `/openapi.json`), and a summary of its current content.

**Quick start:** `make dev`, then open **http://localhost:5173** and sign in with the dev key
`dev-sandbox-key`. Everything else is below.

## Running locally

It's **one binary** — `intraktible` — that runs the whole platform or any subset, with pluggable
storage. The different "ways to run it" are just flags on the same `serve` command; pick a path.

**Prerequisites:** Go **1.26+** (backend) · Node **20+** (only to build or dev the UI) · Docker (optional).

### Fastest path — full-stack dev with hot reload

```sh
make dev
```

Starts the Go API on **:8080** and the SvelteKit dev server on **:5173** (hot-reload, proxying API
calls to the backend) in one command. Open **http://localhost:5173** and sign in with the seeded dev
key **`dev-sandbox-key`**. Ctrl-C stops both.

### Run the single binary (UI embedded, production-like)

```sh
make run                       # build + serve everything → http://localhost:8080
go run ./cmd/intraktible serve # no build step; serves the full API + a placeholder UI page
make dist && ./bin/intraktible serve   # self-contained artifact: the real UI baked into the binary
```

These serve the embedded SPA on **:8080** — no Node needed at runtime.

### One command, many ways — `serve` flags

| Flag | Default | What it does |
| --- | --- | --- |
| `--modules` | `all` | Which modules run — `all`, or a comma list of `decision-engine,case-manager,context-layer,agent-manager,hello` |
| `--store` | `memory` | Projection store: `memory` (ephemeral, rebuilt from the log) · `sqlite` (durable, `<data-dir>/projections.db`) · `postgres` (`INTRAKTIBLE_POSTGRES_DSN`) |
| `--log` | `file` | Event log: `file` (single-process WAL) · `sqlite` (shared across processes — used by the split profile) · `postgres` (`INTRAKTIBLE_POSTGRES_DSN`) · `nats` (JetStream HA, `INTRAKTIBLE_NATS_URL`) |
| `--addr` | `:8080` | Listen address |
| `--data-dir` | `./data` | Where the event log (and the SQLite store) live |
| `--dev-api-key` | `dev-sandbox-key` | Seed a dev admin key — **in-memory store only**; ignored with a durable store, so production never boots with it (set empty to disable) |

```sh
intraktible serve                                       # modular monolith, in-memory
intraktible serve --modules=decision-engine             # run just one module
intraktible serve --store=sqlite                         # projections survive restarts
INTRAKTIBLE_POSTGRES_DSN=postgres://… intraktible serve --store=postgres
```

Projections always rebuild from the append-only event log on boot, so your data survives a store swap.

### Operate the log (same binary, no server)

```sh
intraktible log                       # print the event log + a per-stream summary
intraktible replay                    # rebuild projections from the log into a fresh store
intraktible replay --as-of 42         # time-travel: rebuild as of seq 42 (read-only rollback)
intraktible export --flow credit --format mermaid   # export a flow: mermaid | mermaid-state | bpmn | dot | json
intraktible export --decision <id> --format dot      # export a recorded run: mermaid | dot | json
```

### Docker (no Go/Node toolchain needed)

```sh
cd deploy
docker compose up                     # monolith → http://localhost:8080
docker compose --profile split up     # one container per module (:8081–:8084), shared SQLite log
                                      # authenticate with the key set in the compose file:
                                      #   curl -H 'X-Api-Key: split-profile-local-dev-key' localhost:8081/v1/me
docker compose --profile pg up        # add a Postgres projection store
```

**Production** — deploy on Kubernetes with the Helm chart in [`deploy/helm/intraktible`](deploy/helm/intraktible)
(API tier + scalable durable-worker tier + singleton scheduler tier,
`/healthz`+`/readyz` probes, HPA/PDB, hardened securityContext), or single-host
with [`deploy/docker-compose.prod.yml`](deploy/docker-compose.prod.yml).
Set `INTRAKTIBLE_ENV=production` and the server refuses to boot on insecure config. Full
runbook: [docs/DEPLOY.md](docs/DEPLOY.md); backups/DR: [docs/DR.md](docs/DR.md).

### Configuration (environment variables)

| Variable | Purpose |
| --- | --- |
| `INTRAKTIBLE_AI_BASE_URL` · `_API_KEY` · `_MODEL` · `_PROVIDER` | Use a real OpenAI-compatible AI provider. Without one, AI nodes/agents/copilot **fail loudly** |
| `INTRAKTIBLE_AI_STUB` | Opt in to the deterministic canned Stub provider (dev/tests only — never silently substituted) |
| `INTRAKTIBLE_AI_PRICES` | Per-model token prices (e.g. `gpt-4o=2.5/10`, USD per million input/output tokens) to derive AI run cost on the Observability page |
| `INTRAKTIBLE_POSTGRES_DSN` | Postgres DSN for `--store=postgres` / `--log=postgres` |
| `INTRAKTIBLE_NATS_URL` | NATS server URL for `--log=nats` (JetStream; the account must create/update the event stream, publish `intraktible.events` + `intraktible.events.claim.*`, and support multi-filter consumers) |
| `INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE` | Let HTTP connectors reach private/loopback hosts (off by default — SSRF guard) |
| `INTRAKTIBLE_OTEL_EXPORTER` · `_SAMPLE_RATIO` | OpenTelemetry tracing: `stdout` or `otlp` (off by default; OTLP endpoint via the standard `OTEL_EXPORTER_OTLP_*` vars) |
| `INTRAKTIBLE_AI_RATE_LIMIT_RPS` · `_BURST` | Per-provider AI rate limit (token bucket; off by default) |
| `INTRAKTIBLE_AI_GUARDRAIL_PII` · `_REDACT_FIELDS` · `_BLOCK_INJECTION` | AI guardrails: redact PII in prompts/output, mask structured fields (CSV), block prompt-injection (off by default) |
| `INTRAKTIBLE_LOGIN_RATE_LIMIT_RPS` · `_BURST` | Per-client login token bucket (defaults 10 requests/s, burst 30; RPS `0` disables). Invalid/negative values, or a zero burst while enabled, refuse startup |
| `INTRAKTIBLE_PROCESS_ROLE` | Background ownership: `all` (single-process default), `api`, `worker`, or `scheduler`. Production HA uses separate roles so API replicas never recover durable work |
| `INTRAKTIBLE_DECISION_RECOVERY_INTERVAL` | Positive worker scan cadence for interrupted decisions (default `5s`); malformed/non-positive values refuse startup |
| `INTRAKTIBLE_MONITOR_INTERVAL` · `INTRAKTIBLE_MODEL_DRIFT_WINDOW` | Scheduler cadence (e.g. `1m`) and optional recent model-drift window in non-negative days; malformed values refuse startup |
| `INTRAKTIBLE_DECIDE_EVAL_TIMEOUT` | Positive Go duration limiting expression/Code evaluation per decision (for example `2s`); malformed/non-positive values refuse startup |
| `INTRAKTIBLE_ENCRYPTION_KEY` · `_KEYS_PREVIOUS` | Encryption at rest for event payloads + projection store (base64/hex 32-byte key; previous keys retained for zero-downtime rotation; off by default) |
| `INTRAKTIBLE_KMS_PROVIDER` · `_KEY` | Seal connector credentials via an external KMS (`aws`\|`gcp`) so the key never leaves the provider |
| `INTRAKTIBLE_BOOTSTRAP_API_KEY` | Seed an operator-chosen admin key on **any** store (≥16 chars), so a self-hosted install can get its first credential without SSO. Re-added each boot; rotate to a managed key and unset it |
| `INTRAKTIBLE_SINGLE_REPLICA` | Declare that exactly one replica runs, permitting `--log=file` in production. Without it a production boot with the single-process WAL is **refused** — each replica would keep a divergent copy of the event log |
| `INTRAKTIBLE_DRAIN_DELAY` · `_SHUTDOWN_TIMEOUT` | Shutdown timing (defaults 5s · 30s). On `SIGTERM` the replica fails `/readyz` but keeps serving for the drain delay — so the load balancer depools it before the listener closes — then finishes in-flight requests within the timeout. Both are validated at startup |

Explicit boolean switches accept only `1/0`, `true/false`, `yes/no`, or `on/off`;
typos refuse startup rather than silently changing security or runtime behavior.

### See it end-to-end

```sh
./examples/demo.sh                    # seeds context + an agent, runs a flow, opens a case
```

A narrated walkthrough is in **[docs/EXAMPLE.md](docs/EXAMPLE.md)**. Quality gates: `make check` (fast)
or `make ci` (the full gate CI runs).

## License
**AGPL-3.0-or-later** — see [LICENSE](LICENSE). Every dependency must be AGPL-compatible
(MIT/BSD/ISC/Apache-2.0/MPL-2.0 or compatible copyleft); **SSPL, BUSL/BSL, Elastic License, Commons
Clause, and GPL-2.0-only are disallowed**, enforced in CI (`go-licenses` + `license-checker`).
Policy & vetted-deps table: [docs/LICENSING.md](docs/LICENSING.md). As a network service, AGPL §13
applies — hosted instances must offer their source.

> Independent reimplementation of the *concepts* — not affiliated with or derived from any vendor's
> code/assets. Research basis is in the parent directory (`../specs/`, `../docs/`).
