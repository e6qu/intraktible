# intraktible — Implementation Plan

> Open-source MVPs of the four user-facing components of a commercial **Agentic Decision
> Platform**. Source of truth for scope and architecture; updated at the end of every phase
> (alongside `BUGS.md`). **New here? Start with [AGENTS.md](AGENTS.md).**

Research basis: the reverse-engineered understanding of the reference platform lives one level up
(`../products/`, `../specs/`, `../ENDPOINTS.md`, `../docs/`). `intraktible` is an
independent open-source reimplementation of the *concepts*, not a copy of any vendor's code or assets.

**License: `AGPL-3.0-or-later`** ([`LICENSE`](LICENSE), policy in [`docs/LICENSING.md`](docs/LICENSING.md)).
**Every dependency must be AGPL-compatible** — permissive (MIT/BSD/ISC/Apache-2.0/MPL-2.0) or
compatible copyleft (LGPL, GPL-2.0-or-later, GPL-3.0+, AGPL). **Disallowed:** SSPL, BUSL/BSL,
Elastic License, Commons Clause, GPL-2.0-*only*, and any proprietary/source-available license.
Enforced in CI via `go-licenses` (Go) and `license-checker` (web); a disallowed dep fails the build.
As a network service, **AGPL §13** applies — hosted instances expose a source offer (UI + `/source`).

---

## 1. The four components (each a subdirectory; MVPs)

| Dir | Component | One-liner |
|---|---|---|
| `decision-engine/` | **Decision Engine** | Drag-and-drop builder + execution runtime for versioned decision flows |
| `case-manager/` | **Case Manager** | Queues + dashboards for human review of escalated decisions |
| `context-layer/` | **Context Layer** | Entities/events/features data model + connectors (data marketplace) |
| `agent-manager/` | **Agent Manager** | Configure/run/monitor task agents (LLM + tools) inside flows |

Plus shared infrastructure in `platform/`, the binary in `cmd/intraktible/`, and the UI in `web/`.

---

## 2. Locked decisions (from requirements gathering)

- **Backend:** Go, **functional core / imperative shell** (pure decision/domain logic; side effects
  only at the edges). Strict linting, **dead-code detection**, **copy-paste detection** in CI.
- **Frontend:** **SvelteKit + Svelte Flow (`@xyflow/svelte`)**, TypeScript. Single SPA serving all
  four module UIs.
- **Architecture:** **modular monolith that is also separable into services** — one binary runs all
  modules (`intraktible serve --modules=all`) or any subset; each module can also run standalone.
- **Data:** **not relational-CRUD-centric.** A **pure-Go embedded append-only event log (WAL /
  BadgerDB)** is the backbone; **hybrid event sourcing** — events are the source of truth, current
  state is a **JSONB materialized projection** rebuilt from the log. **Perfect replay + log-based
  rollback.** Storage is **pluggable** (SQLite and Postgres) and **schema-dynamic (JSON/JSONB)**
  except where a fixed schema clearly makes sense (e.g. the event-envelope, auth).
- **AI:** **pluggable provider interface** (Claude / OpenAI / Gemini / Ollama swappable).
- **Build sequence:** **shared core first → Decision Engine end-to-end → Case Manager → Context
  Layer → Agent Manager.**
- **Multi-tenancy:** **org + workspace scoping from day 1.** Every event and projection is scoped
  to `(org_id, workspace_id)`; event streams are partitioned per workspace. Mirrors the reference platform's
  workspace/org/`{workspace}.{org}.decide` model and keeps replay/rollback per-tenant.
- **Web delivery:** the SvelteKit UI is **built to static assets and embedded in the Go binary**
  (`embed.FS`) — one self-contained artifact serves API + UI. (Dev uses the Vite dev server
  proxying the Go API; prod embeds.)
- **Auth:** **API keys (sandbox/production scopes) for the decide/data APIs + a minimal session
  login** for the builder UI. Pluggable SSO/OIDC later.

### Engineering principles
- **Fail loudly** in logic — no silent fallbacks, empty catches, or "log & continue" in core logic
  (retries/backoff for genuine network unreliability are fine).
- **Deterministic core** — decision execution must be reproducible from recorded inputs (prerequisite
  for replay). No wall-clock/random in the core except via injected, recorded effects.
- Keep phase/issue references in commit messages and these docs, **not in source comments**.

---

## 3. Architecture

### 3.1 The log/stream backbone (`platform/eventlog`)
An append-only, ordered, partitioned **event log** with a small interface:

```
Append(stream, event) -> (offset, error)
Read(stream, fromOffset) -> iterator
Subscribe(stream, fromOffset) -> channel        // in-process bus for the monolith
```

- Default implementation: **pure-Go**, backed by **BadgerDB** (or a segmented WAL) — zero external
  deps, embeddable in the single binary.
- The interface is **pluggable** so a distributed backend (NATS JetStream / Kafka / Redpanda) can be
  dropped in for the split-services deployment without touching domain code.
- Events are immutable JSON envelopes: `{id, org_id, workspace_id, stream, type, time, actor, seq,
  payload(JSON)}`. Streams are partitioned per `(org_id, workspace_id)` so replay/rollback is
  per-tenant.

### 3.2 CQRS / hybrid event sourcing
- **Commands** (write side) validate against the functional core, then **emit events** to the log.
- **Projections** (read side) consume the log and maintain **materialized JSONB state** in the
  pluggable store (SQLite JSON1 / Postgres JSONB). Projections are **rebuildable** from offset 0.
- **Replay** = re-fold events (rebuild a projection) or **re-run a decision** from its recorded
  input event (deterministic core ⇒ identical result, or a diff if logic changed).
- **Rollback** = move a projection/aggregate to a prior offset (the log is never mutated; we roll the
  *view*), or compensating events.

### 3.3 Decision execution as a logged stream
Each `/decide` call becomes a **DecisionStarted** event; every node execution emits a
**NodeEvaluated** event (inputs, output, duration); completion emits **DecisionCompleted** (or
**DecisionFailed**). This *is* the Decision History (replayable node-by-node), mirroring the
`DecisionRecord` shape we documented (`flow{slug,version}`, `node_results.time_ordered`, etc. — see
`../specs/data-models.md`).

### 3.4 Functional core / imperative shell (per component)
```
<component>/
  domain/      # PURE: types, decision logic, fold/reduce, validation. No I/O.
  events/      # event type definitions (JSON payloads)
  command/     # command handlers: validate (pure) -> emit events (shell)
  projection/  # event -> JSONB read-model builders
  service/     # imperative shell: HTTP handlers, wiring, adapters
```

### 3.5 Deployment shapes
- **Monolith:** one process; in-process event bus; one embedded log; SQLite by default.
- **Split:** each module its own process; shared distributed log; Postgres. Same code, different
  wiring in `cmd/`.

---

## 4. Component MVP scope

### 4.1 Decision Engine (`decision-engine/`) — built first, end-to-end
- **Flow model:** a flow = DAG of typed **nodes** + edges; **versioned** (immutable versions, etag),
  each version carries an `input_schema` (JSON Schema) — the per-flow decide contract.
- **Node types (MVP):** Input/Start, **Rule**, **Split**, **Assignment**, **Scorecard**,
  **Decision Table**, **2D Matrix**, **Code**, **AI** (→ Agent Manager), **Connect** (→ Context
  Layer), Output/End. Logic engines: **CEL-go** (conditions), **expr-lang** (assignments/expr),
  **Starlark-go** (Code node — Python-like, deterministic).
- **Builder UI:** Svelte Flow drag-and-drop canvas, node palette, per-node config panels, inline
  **test runs** (`/author/test-run` analog) with sample data.
- **Execution API:** `POST /v1/flows/{slug}/{env}/decide` with `{data, metadata, control}` →
  decision result + `decision_id`; sandbox/production environments; **X-Api-Key** auth.
- **Decision History:** list/query past decisions (paginated) with full node-level replay.
- **Optimization (lite):** A/B (champion/challenger) routing + a simple analytics projection.

### 4.2 Case Manager (`case-manager/`)
- Cases created when a flow emits **ManualReviewRequested**; queues filtered by type/status/assignee.
- Case object (dynamic JSONB): `company_name, assignee, status(needs_review|in_progress|completed),
  sla_days_left, case_type, created_at, updated_at, context`. Dashboard + case detail + audit log
  (all from events). Assignment, status transitions, notes — all emit events.

### 4.3 Context Layer (`context-layer/`)
- **Custom Entities / Events / Features** (dynamic JSONB schema). A **feature engine** computing
  real-time signals from the event stream (windowed counts/sums) consumed by Rule nodes.
- **Connectors:** a `Connect` interface + a few reference connectors (HTTP/REST, SQL, a mock bureau)
  and a **Custom Connect Node** for arbitrary HTTP APIs. Connector results recorded as events.

### 4.4 Agent Manager (`agent-manager/`)
- An **agent** = config over the pluggable **AI provider** + a tool set + a **structured-output
  schema** (name/type/description), invoked by the flow's **AI node**. Run logs, human-in-the-loop
  escalation (→ Case Manager), monitoring projection. Bring-your-own model via the provider iface.

---

## 5. Cross-cutting (`platform/`)
- `eventlog/` — append-only log + bus (§3.1).  `projection/` — projection runtime + rebuild.
- `store/` — pluggable KV/JSONB store (SQLite, Postgres adapters).  `schema/` — JSON Schema
  validation, dynamic types.  `ai/` — provider interface + adapters (Claude/OpenAI/Gemini/Ollama).
- `httpx/` — server, routing (std `net/http` 1.22 mux or chi), middleware (auth, request-id).
- `auth/` — **API keys (sandbox/production scopes)** for the decide/data APIs + a **minimal session
  login** for the builder UI; **org/workspace** identity on every request; pluggable SSO/OIDC later.
- `telemetry/` — structured logs + OpenTelemetry traces.

---

## 6. Candidate tech (validate in Phase 0)
Go: BadgerDB (log), `cel-go`, `expr-lang/expr`, `starlark-go`, `pgx` + `modernc.org/sqlite`,
std `net/http`/`chi`. Frontend: SvelteKit, `@xyflow/svelte`, TypeScript, Vitest/Playwright.
Tooling: `golangci-lint`, `golang.org/x/tools/cmd/deadcode`, `dupl` (Go) + `jscpd` (web) for
copy-paste, `govulncheck`, **`go-licenses` + `license-checker`** for license compliance. ML node
(ONNX via CGO) is **optional/stubbed** for MVP. **All of the above are AGPL-compatible** (permissive
MIT/BSD/Apache-2.0); see the vetted table in [`docs/LICENSING.md`](docs/LICENSING.md).

---

## 7. Repository layout
```
intraktible/
  PLAN.md  BUGS.md  README.md  go.work
  cmd/intraktible/        # single binary: `serve --modules=...`, per-module subcommands
  platform/              # shared core + shell (eventlog, store, projection, schema, ai, httpx, auth)
  decision-engine/       # domain/ events/ command/ projection/ service/
  case-manager/
  context-layer/
  agent-manager/
  web/                   # SvelteKit SPA (module routes: /engine /cases /context /agents)
  tools/                 # lint/dupl/deadcode configs + scripts
  deploy/                # docker-compose (monolith + split profiles)
  docs/                  # ADRs, API docs
```

---

## 8. Phased roadmap
- **Phase 0 — Core & scaffolding — ✅ DONE.** Shipped: AGPL `LICENSE` + SPDX headers on every file
  + license CI (`go-licenses`/`license-checker`); single Go module; `platform/eventlog` (pure-Go
  file WAL + in-proc bus, durable & replayable); `platform/store` (in-memory JSONB projection store);
  `platform/projection` (rebuild-from-offset-0 + live consumer); `platform/identity` (org/workspace
  scoping); `platform/auth` (API keys sandbox/prod + minimal sessions); `platform/httpx` (server,
  request-id, recover, logger, auth middleware); `platform/ai` (pluggable provider + Stub);
  `platform/web` (`embed.FS` UI); the **`hello`** vertical slice (domain/events/command/stats/service)
  proving command→event→projection→API→UI with restart-replay; tests (race) green; `cmd/intraktible
  serve --modules`; Makefile + golangci-lint + CI workflow; Dockerfile + docker-compose; SvelteKit
  scaffold. **Deferred from Phase 0** (tracked in `BUGS.md`): Badger backend (WAL used instead — open
  Q1), durable SQLite/Postgres projection stores, JSON-Schema validation lib, Claude AI adapter
  (Stub only), and running the SvelteKit build into the embed dir (Go placeholder UI serves for now).
- **Phase 1 — Decision Engine — ✅ DONE.** Shipped: flow model + immutable etag'd versioning; a
  deterministic execution runtime over nine node engines (Input/Assignment/Rule/Split/Scorecard/
  Decision Table/2D Matrix/Code/Output — expr-lang for expressions, Starlark for the Code node)
  emitting the decision event stream (DecisionStarted→NodeEvaluated→Completed/Failed); the
  `…/{env}/decide` API; decision history; per-environment version pinning + A/B (champion/challenger)
  routing; analytics-lite (per-flow metrics with variant breakdown); and the Svelte Flow builder UI
  (`web/src/routes/engine`) — flow list/create, graph editing (palette, edges, per-node config,
  publish with backend validation), canvas view (auto-layout), and inline test runs. Full test
  pyramid (unit/integration/API-e2e/Playwright); all CI gates green. **Deferred from Phase 1** (in
  `BUGS.md`): CEL conditions (expr-lang + Starlark already cover conditions — D9), builder UI polish
  (drag-connect + bespoke config panels — D10), per-node decide appends (D11); still open from before:
  embedding the production UI build (D6) and decide-input schema validation (D4).
- **Phase 2 — Case Manager — ✅ DONE.** Shipped: case lifecycle (ReviewRequested → assign /
  status / notes) as command→event→projection→API; the `cases` read model with a per-case audit log
  built from events; queue listing filtered by status/type/assignee; the **escalation hook** — a
  decision flow's `manual_review` node makes the engine emit `decision.manual_review_requested`,
  which the Case Manager consumes to open a case linked by `source_decision_id` (cross-component via
  the event log only); **SLA tracking** — days-left + on_track/due_soon/overdue state computed at the
  read boundary (the stored projection stays clock-free + replay-stable) plus a **queue summary**
  roll-up (`GET /v1/cases/summary`: totals by status, unassigned, due-soon, overdue); and the
  **dashboard UI** (`web/src/routes/cases`) — queue with filters, a summary banner, and per-row
  days-left, plus case-detail with assign/status/note actions and the audit log. Full test pyramid
  (unit/integration/API-e2e/Playwright); all CI gates green. **Deferred from Phase 2** (in `BUGS.md`):
  no SLA-breach events/alerts — overdue is derived on read (D12); no rich/schema-aware context view in
  case detail (D13).
- **Phase 3 — Context Layer — ✅ DONE.** Shipped: **custom entities** (dynamic JSONB keyed by
  type+id; re-recording patches via a pure top-level attribute merge) and **custom events** about an
  entity (per-entity event log + a running event count; an event auto-creates a shell entity;
  `occurred_at` is a recorded effect for replay stability); and a **feature engine** — windowed
  `count`/`sum` aggregates over an entity type's event stream (definition =
  `{name, entity_type, event_name, aggregation, field?, window_hours}`; the pure `domain.Compute`
  folds events in `(now-window, now]`; missing sum-field contributes 0, non-numeric fails loudly),
  computed read-time so the log stays clock-free — command→event→projection→API
  (`/v1/context/entities`, `…/{type}/{id}[/events|/features]`, `/v1/context/events`,
  `/v1/context/features`), module `context-layer`. Features are **wired into the decision engine**: a
  decide call may carry an `{entity_type, entity_id}` ref; the shell computes that entity's features
  and folds them into the input under `features.*` (so any Rule/Split/etc. expression can read
  `features.txn_count_24h`), recorded in `DecisionStarted` for replay stability. The engine stays
  free of a context-layer import via a `FeatureProvider` **port** (in `decision-engine/command`)
  satisfied by a `features.Provider` **adapter** wired at the composition root — preserving the
  build-order dependency direction. **Connectors** subsystem: a `Connect` interface + registry +
  reference connectors (an arbitrary-HTTP one and a deterministic `mock_bureau`); a definition is
  `{name, type, config}` and invoking a connector is an effect recorded as a `ConnectorFetched`
  event (the stored response, not a re-fetch, is what replay reads) — API `/v1/context/connectors`
  + `…/{name}/fetch` + `…/{name}/fetches`. A flow's **Connect node** is wired the same way as features:
  the shell pre-resolves each connector (params = the current input), injects the response under
  `connect.<output>`, and records it in `DecisionStarted` — via a `ConnectorProvider` port +
  `connectors.Provider` adapter, so the pure core does no I/O and the engine never imports the Context
  Layer. Full test pyramid (unit/integration/API-e2e); all CI gates green. **Deferred from Phase 3**
  (in `BUGS.md`): a **SQL** reference connector (D14); an SSRF/egress policy for the HTTP connector
  (D15).
- **Phase 4 — Agent Manager — ✅ DONE.** Shipped: **agent definitions** (a config over the
  pluggable AI provider — `name`, optional `provider`/`model`, `system` prompt, optional
  structured-output JSON `schema`, declared `tools`) and **agent runs** (invoking the provider with
  that config + a prompt; the response — text or schema-constrained structured output — is captured in
  an `AgentRunRecorded` event so a run is auditable and replay reads the recorded output, not a re-call
  of the non-deterministic model; a provider failure is a recorded `failed` run). Command→event→
  projection→API: `/v1/agents` (+`/{name}`), `/v1/agents/{name}/run`, `/v1/agents/{name}/runs`,
  `/v1/agent-runs/{run_id}`; module `agent-manager`. A real OpenAI-compatible HTTP AI provider ships (env-configured); the Stub is the default fallback.
  Enabling refactor: hoisted `eventlog.AppendJSON` (the marshal→append spine). A flow's **AI node** runs
  an agent during a decision: the shell pre-resolves it (the node's literal prompt, or the current
  input) and injects the output under `ai.<output>`, recorded in `DecisionStarted` — via an
  `AgentProvider` port + `agents.Provider` adapter wired at the composition root, so the engine never
  imports the Agent Manager (same one-way wiring as features/connectors). **Human-in-the-loop**:
  escalating a run opens a Case Manager case — the Agent Manager (built later) emits the Case Manager's
  own `ReviewRequested` event the `cases` projector already consumes, with the run in the case context
  (one-way direction, no `cases` change). **Monitoring**: `GET /v1/agent-runs/summary` rolls up the run
  log (totals, completed/failed, by agent). The **agents UI** (`web/src/routes/agents`) lists/defines
  agents with a run-summary banner, and a per-agent view runs the agent, shows the run log, and
  escalates a run. Full test pyramid (unit/integration/API-e2e/Playwright); all CI gates green.
  **Deferred from Phase 4** (in `BUGS.md`): tools are declared but not executed (D16); runs are
  synchronous and structured output is not schema-validated (D17); real (non-Stub) AI providers (D5).
- **Phase 5 — Harden — ✅ DONE.** Shipped: **replay/rollback operator tooling** — `intraktible
  log` prints the durable event log (one line per event) + a per-stream summary (the audit view), and
  `intraktible replay [--modules] [--as-of <seq>]` rebuilds the enabled modules' projections from the
  log into a fresh store and reports the rebuilt collections. `--as-of` is a read-only **log-based
  rollback** (rebuild as of an earlier seq), backed by `projection.RebuildTo(ctx, upTo)`; the
  append-only log is never mutated. The CLI dispatches `serve|log|replay`, and `serve`/`replay` share
  one `moduleProjectors` list. The **split-services** deploy profile (`deploy/docker-compose.yml`
  `--profile split`) runs one container per module (same image, `serve --modules=<name>`). A worked
  end-to-end **example** ([`examples/demo.sh`](examples/demo.sh) + [`docs/EXAMPLE.md`](docs/EXAMPLE.md))
  exercises all four components + the operator tooling. The split-services profile now shares one
  durable **SQLite event log** (`serve --log=sqlite`) so cross-component flows work across processes (D18).

**MVP roadmap complete (Phases 0–5), plus a post-MVP hardening pass.** The hardening pass closed the
bulk of the `BUGS.md` backlog: durable SQLite projection store + a shared SQLite event log + a
**Postgres** store adapter, a streaming (offset-indexed) file WAL, a real OpenAI-compatible AI provider
with agent **tool-calling** and **async/queued runs**, the production UI embedded as an SPA,
login/durable sessions, a recursive JSON-Schema validator, an SSRF egress policy + a SQL connector for
the Context Layer, pushed SLA-breach events, and full builder config panels (incl. the nested-table
node types) + canvas drag-to-connect. What remains in `BUGS.md` is the small tail: incremental
resume-from-Head for durable stores (D21b, a consistency project), the closed-by-decision items
(D9 CEL, D11 batched events), and the §9 non-goals (SSO, billing, 200+ real connectors, ONNX
serving, HA).

**Enterprise-readiness track (post-MVP, ongoing).** Beyond the §9 non-goals, an enterprise-readiness
pass began closing the gaps a regulated rollout needs (tracked in [`docs/ENTERPRISE.md`](docs/ENTERPRISE.md)).
Shipped so far: **RBAC** (`platform/auth` role hierarchy viewer→admin + `platform/httpx` per-request
authorization) plus admin-managed durable API tokens (`GET/POST/DELETE /v1/api-keys`, hashed at rest,
scoped by org/workspace, role, scope, actor, optional expiry, and revocation), **maker-checker /
four-eyes approvals** for production deploys (propose-by-one,
approve-by-a-different-user, recorded on the flow as an auditable trail) plus a **promotion workflow**
(sandbox → staging → production; `POST …/promote {from,to}` ships the live version up the chain — direct
into non-prod, maker-checker request into prod) with per-stage promotion policy (`GET/PUT
…/promotion-policy` controls assertions/monitors/review/force gates), **backtesting** —
`POST /v1/flows/{flow_id}/backtest` (`decision-engine/backtest`, pure) replays a dataset through a
published version and optionally diffs two versions over `domain.Execute` only (no recorded decision,
no I/O), surfaced in the builder as a panel that flags the changed records — and the **immutable audit
surface** (`GET /v1/audit`, `platform/audit`): a tenant-scoped, filterable, CSV-exportable read over the
event log, admin-gated, with an Audit log UI page; and **reason codes** — a Reason node (`decision-engine/
domain`) emits structured adverse-action `{code, description}`s into a reserved `reason_codes` field
(always surfaced by Output), which the history projector lifts to a first-class field on the decision
record and the decision UI shows (ECOA/Reg B + insurance explainability). **All five enterprise P0 items
are done.** Connector credential fields are encrypted before connector-definition events when
`INTRAKTIBLE_CONNECTOR_SECRET_KEY` is configured, with keyring-based key rotation
(`…_KEYS_PREVIOUS`) and an **external KMS** option (`platform/kms`, AWS KMS / GCP Cloud KMS via
`INTRAKTIBLE_KMS_PROVIDER`); remaining P1/P2 work is
broader encryption-at-rest/retention, alerting polish, SCIM, SOC2 — sequenced in
`docs/ENTERPRISE.md`.

**Decision-automation layer (post-MVP).** A shared disposition brain now sits over the engine:
**policies** (`decision-engine/policy`) attach auto-approve/decline/refer bands to a flow and assign a
disposition on every decision (real-time STP), with a record-nothing **disposition backtest** for safe
tuning; **batch decisioning** (`…/{env}/decide/batch`) scores a whole population through the recorded
decide path; and **pre-approvals** (`decision-engine/preapproval`) are durable, time-boxed grants per
entity that the decide path **honors instantly** — a pre-approved entity is completed straight from the
grant's terms, skipping the flow, recorded with `preapproval_id` for provenance. The three modes join up
via **promote-to-pre-approvals** (`…/{env}/preapprove/batch`): a population runs through the policy and
every approved row becomes a durable grant keyed by a row field — decide a population once, pre-approve
the winners, honor them instantly. UI: `/policies` (band editor + impact preview), `/preapprovals`
(grant / list / revoke), and a **Promote to pre-approvals** panel in the builder.

**Monitors (post-MVP, observability).** `decision-engine/monitor` adds threshold rules over a flow's live
metrics — failure / refer / automation / approve / decline rate, avg latency, and volume — each
`{metric, op, threshold}` evaluated **firing/ok** against the analytics projection at read time (a pure
function of the snapshot; a rate with no data reads "no data", not a false 0). `POST|GET|DELETE
/v1/flows/{id}/monitors` (define editor-gated); a **Monitors** panel in the builder defines rules and
shows live status. **Notifications** (`decision-engine/notify`) make them actionable: register webhooks
(`POST /v1/webhooks`) and a monitor **check** (`POST /v1/flows/{id}/monitors/check`) pushes the firing set
to every active webhook over the SSRF-safe egress client, recording each delivery for audit. **Distribution
drift** is a first-class metric: `POST /v1/flows/{id}/baseline` snapshots the disposition mix, `GET
…/drift` reports per-bucket shift, and a `distribution_drift` monitor alerts on it. A **scheduler**
(`monitor.Scheduler`, `INTRAKTIBLE_MONITOR_INTERVAL`) sweeps on a timer and notifies only on the ok→firing
edge (resetting on resolve). The alerting gap is closed end-to-end — rules + drift + delivery + scheduling.

**Flow assertions + promotion gates (post-MVP, governance).** `decision-engine/assertions` stores
input→expected test cases per flow (`PUT/GET /v1/flows/{id}/assertions`, `POST …/assertions/run`), run
through the pure backtest core. The **promotion gate** refuses a promote (409) when the flow's monitors
are firing or its assertions fail on the target version — `force` overrides. Surfaced as an Assertions
panel + a force toggle in the builder. Ties monitors + tests into the sandbox→staging→production chain.

**Shadow deploys (post-MVP, safe rollout).** `decision-engine/shadow` adds a per-environment **shadow
version** (`PUT/GET /v1/flows/{id}/shadow`) evaluated over the same input as every live decision in that
environment via the pure core — its result is never returned. A projector folds each comparison into a
per-env report (total / matched / diverged / errored + sample diverging decision ids), so an operator can
measure how often promoting a candidate would change the outcome before doing it. Surfaced as a **Shadow
deploys** panel in the builder; complements the A/B challenger (which serves a traffic share live).

**API contract (post-MVP, developer experience).** `platform/openapi` embeds an **OpenAPI 3.1** document
for the public data-plane (decide + batch, decision history, flow list/create/read, flow-as-code import,
`/v1/me`) and serves it unauthenticated at `GET /openapi.json`, with a dependency-free reference page at
`GET /docs`. Integrators point codegen/Swagger-UI/Postman at the live instance's own contract. A typed
**Go client SDK** (`client`) wraps that surface (decide/batch, decision history, flow management) over
net/http with no third-party deps and a typed `*APIError`, tested end-to-end against a live engine. A
matching **TypeScript SDK** (`web/src/lib/sdk.ts`, fetch-only and framework-agnostic) ships the same
surface for browser/Node/edge consumers; packaging the SDKs for distribution is the next step.

**Networked event log (post-MVP, HA).** `eventlog.OpenPostgresLog` (`--log=postgres`,
`INTRAKTIBLE_POSTGRES_DSN`) is a durable, shared log for true multi-node HA: every node appends to and
reads from one Postgres database, a `BIGSERIAL` seq gives a single total order across nodes, and a shared
polling `delivery` (factored out of the SQLite log) fans any node's newly-committed events onto each
process's in-process bus, with a **LISTEN/NOTIFY fast path** (each append `NOTIFY`s; a dedicated listen
connection pokes delivery) so cross-node delivery is near-instant rather than poll-bound — the poller
stays as the correctness floor. Read/Head are immediately consistent; verified against a real Postgres
including cross-node delivery and sub-poll NOTIFY latency. A **NATS JetStream** backend
(`eventlog.OpenNATSLog`, `--log=nats`) is the other networked option — the stream sequence is the event
Seq and a push consumer delivers live with no poller (verified against an embedded JetStream server).
Kafka remains.

**SSO / OIDC (post-MVP, enterprise identity).** `platform/auth.OIDCAuthenticator` + `platform/httpx`
`/v1/auth/oidc/{provider}/login|callback` add OIDC Authorization-Code SSO: the IdP's ID token is
verified against its JWKS (issuer/audience/expiry + nonce) via `coreos/go-oidc`, a state cookie covers
CSRF, and the verified email plus a configurable **groups-claim → role** mapping issues a normal session.
Providers are env-configured (`INTRAKTIBLE_OIDC_PROVIDERS`); **Google** and **AWS Cognito** ship with
sensible defaults. The login page renders a "Sign in with …" button per provider. **SCIM** provisioning (`platform/scim`,
`/scim/v2/Users`, bearer-authed) is the companion: an IdP creates/deactivates users and the OIDC login
consults it through a gate, so a user deactivated in the IdP is refused a session (deprovisioning). SCIM
**Groups** (`/scim/v2/Groups`) plus a group→role map (`INTRAKTIBLE_SCIM_GROUP_ROLES`) additively elevate a
user's role from their SCIM group membership at login (highest of token- and SCIM-derived wins).

**SAML 2.0 SSO (post-MVP, enterprise identity).** A second SSO protocol alongside OIDC:
`platform/auth.SAMLAuthenticator` (via `crewjam/saml`) + `platform/httpx`
`/v1/auth/saml/{provider}/{login,acs,metadata}` run the SP-initiated flow — relay-state CSRF, the ACS
verifies the signed SAMLResponse against the IdP metadata (signature/conditions/audience/InResponseTo),
and an email + groups-attribute → role mapping issues a session, sharing the SCIM deprovisioning gate and
group→role augmenter with OIDC. SP cert/key + IdP metadata are file/env-configured.

**Comment threads (post-MVP, governance).** `platform/comments` is a general discussion capability — a
durable, chronological thread keyed by `(subject_type, subject_id)` (`GET/POST /v1/comments/{type}/{id}`),
reusable `CommentThread.svelte` component — wired onto the items that get approved/rejected/promoted
(deployment requests), flows, policies, and decisions, so every reviewable workflow surface carries an
explanation trail. Comments are events (auditable). Posting needs `operator`; reading is open to `viewer`.

**PII masking (post-MVP, compliance).** `platform/privacy` adds a per-workspace sensitive-field list
(`GET/PUT /v1/privacy`, PUT admin-gated) whose values are redacted by a pure masker in decision
input/output, node traces, and exports **at the read boundary** — mirroring connector credential
redaction, so the raw event log stays the source of truth. Managed from the Audit page. Closes the
field-level-masking half of the PII P1; retention/purge + right-to-erasure remain.

**Persona-aware UI (post-MVP).** The web UI gained a **persona** axis (`web/src/lib/persona.ts`) — a
client-side "view-as" preference anyone can switch (not RBAC-gated), orthogonal to light/dark theme. It
applies a `data-persona` attribute that swaps accent, type system, and density, and the landing page
renders a distinct dashboard per persona over the same data: **Builder** (dense monospace command-deck —
flows, latency percentiles, pending deploys, a live decision tape), **Operator** (calm KPI mission-control
— throughput, SLA/queue health, four-eyes approvals, agent runs), and **Showcase** (an editorial serif
story with count-up headline metrics for stakeholders). Typefaces are self-hosted (IBM Plex Sans/Mono +
Fraunces, OFL, vendored under `web/static/fonts` — no runtime CDN). The **Admin surface** (audit ledger)
is deliberately exempt: an `.admin-surface` token set gives it one fixed, canonical slate-indigo identity
for everyone. The Phase-0 hello slice moved off the landing to `/hello`; shared `EmptyState`/`Skeleton`
primitives added designed empty and loading states across the list pages.

**Correctness & security audit pass (post-MVP, hardening).** A codebase-wide audit fixed a real data
**race** in the shared `eventlog` delivery poller (the SQLite/Postgres poller goroutine read the log's
`delivery` field before the constructor published it — `startDelivery` is now `newDelivery`+`start()`,
caught only under `-race`) and a batch of fail-open/fail-loudly gaps: promotion gates now block when
monitor health can't be read; the pre-approval fast path seals PII like the normal decide path;
`privacy.Fields` and the masking callers fail closed on a config-read error; **crypto-shredding** now
recurses into nested objects/arrays and matches field names case-insensitively (mirroring `privacy.Mask`,
so nested PII is actually sealed and erasable); decrypt/unseal failures surface instead of serving raw
sealed envelopes; the monitor scheduler delivers **before** recording the alert (a failed delivery now
retries rather than silencing the alert); `decideBatch` takes a per-row `entity_key` so a multi-entity
batch records under the correct subject; audit CSV export defuses spreadsheet formula injection; and
agent-manager run recovery/enqueue respects context cancellation. Frontend: leaked SSE/WebSocket cleanup
+ error surfacing on the agent page, double-load and stale-route-param fixes across detail pages, a
double-submit guard on case creation, a privacy-config clobber guard on the Audit page, a
stale-response race fix in the command palette, the `manual_review` node made creatable, and split-node
card summaries computed from edges. Verified through the full strict gate (`-race` tests, lint/sast/
deadcode/dupl, svelte-check/eslint/vitest).

**Audit round 2 + builder-parity API (post-MVP, hardening).** A second audit pass (informed by
screenshots of every page across personas/themes) closed more findings and completed the public API for
flow authoring. Security: a viewer could trigger billable agent runs via the GET SSE/WS run endpoints
(the authz layer treated all GETs as reads); OIDC now requires `email_verified` before trusting the email
claim (else falls back to the subject) and both SSO paths reject an empty actor (which would have minted
an anonymous session past the deprovisioning gate); SAML rejects an ambiguous multi-entity metadata
aggregate; the SSRF egress policy also blocks CGNAT (`100.64.0.0/10`); `Identity.Valid` rejects `/` in
org/workspace (tenant-prefix isolation). Correctness: SCIM group PATCH applies all member ops as one
atomic, locked read-modify-write (no partial apply / lost updates); session `Issue` returns an error so a
persist failure fails the login loudly; the SSE/WS run `done` frames include structured output. **API /
builder parity:** flow authoring was already fully expressible over the API, so the gap was (1)
**server-side auto-layout** — a position-less publish/import now gets a deterministic swimlane layout
(new `decision-engine/layout`, a Go port of the builder's `layoutLanes`), preserving any supplied
positions; and (2) **OpenAPI completeness** — the flow graph contract (Graph/Node/Edge schemas, node-type
enum, per-type config), the previously-undocumented `POST …/versions` write, and the control plane are now
documented (11→39 paths). Performance: the NATS log fails loudly on a missing sequence (was a silent skip
that could diverge a rebuild) and is configured for unlimited retention; `flows.BySlug` (decide hot path)
uses a slug→id index with a scan fallback. Deferred to a focused follow-up (to avoid auth/projection
regressions in a large PR): an API-key hash index, a policy-by-flow index, and moving the case-existence
check off its (deliberately consistent) whole-log fold.

**De-scan the auth + decide hot paths (post-MVP, performance).** The three perf items deferred above are
now done. `StoreAPIKeys.ResolveSecret` resolves via a `hash→key-id` index (so a flood of bogus keys can't
amplify into repeated full cross-tenant scans), with a one-time backfill of pre-index keys and a scan
fallback only if that backfill can't complete — a valid key is never wrongly denied. `policy.ActiveForFlow`
(decide hot path) uses a `flow-slug→[policy-id]` index in the policy projector, fetching only the bound
candidates, again with a scan fallback. `caseExists` (hit on every case mutation, under the write lock)
folds the log **incrementally** — an in-memory opened-case set plus the highest folded seq, reading only
new events each call rather than re-scanning from seq 0 — while still reading through to head, so the
deliberate read-after-write consistency (including decision-escalated cases on the shared log) is
preserved. All three keep a fallback / full read so correctness never depends on the index.

**Planned — audit round 3 roadmap (sequenced, healthy-sized PRs).** A third audit (code/security, a UI/UX
review against screenshots of every page × persona × theme, and a competitive + API study vs. comparable
decisioning and BPMN/DMN platforms) produced the backlog in `BUGS.md` (`A1`–`A41`). It is sequenced into
the PRs below — each a substantial, coherent slice (no anemic PRs), one open at a time. Competitor names
never appear in-repo (neutral language only).

1. **Data protection + log usability** (A1–A3) — seal the per-node decision trace so PII is actually
   erasable (HIGH: today node outputs are recorded unsealed and survive a crypto-shred), and make the two
   unbounded logs usable: filter/search/pagination + legible (relative+absolute, sortable) timestamps on
   Decisions and the Audit log (the latter also grouping the high-volume node-evaluated rows).
2. **Engine builder UX** (A4–A8) — stop the single ~3,200px scroll: pin/enlarge the canvas and move
   Test/Backtest/What-if/Assertions/Batch/Promote into tabs or a drawer; canvas polish; prefill/validate
   the raw-JSON inputs from the flow's input schema; a confirm step on Promote; labelled create fields.
3. **Decision explainability + case management** (A9–A12) — surface the decisive branch/rule, per-node
   duration, and reason codes in the decision trace; bulk multi-select assign/close on the case queue, a
   real activity timeline on case detail, labelled queue filters.
4. **Accessibility + visual consistency** (A13–A17) — raise secondary-text contrast to WCAG AA, replace
   placeholder-only inputs with real labels, ensure status isn't color-only, consolidate the top-bar
   identity/role controls (minimal chrome on /login), carry the showcase's typographic hierarchy into the
   working pages, plus breadcrumbs and form-clarity fixes. **Shipped in PR #9:** A13 (AA contrast tokens),
   A14 (real labels on the agents/data forms; status badges already carry text), and A15 (one account &
   view menu + minimal /login chrome) are fixed; A16/A17 partial — the editorial-vs-utilitarian unification,
   breadcrumbs, search-scope placeholder, ≥24px hit areas, and policies band preview remain deferred.
5. **Robustness & bug-fix round** (A18–A31) — backend: NATS Read clamping (FirstSeq/LastSeq + TOCTOU),
   async agent-run off the request goroutine, EscalateRun via the projection, bounded poller read +
   stop-tied context, GCP KMS CRC32C, atomic UpdateDoc on a TxStore, sqlite-connector DSN allowlist,
   Keyring map lookup, SCIM filter parsing, BPMN export id uniqueness; frontend: keyed/reactive policy
   CommentThread, agent-stream cleanup on sibling nav, telemetry-clear, stable BuilderDeck sort.
   **Shipped in PR #10:** all of A18–A31 fixed. Notable refinements vs the sketch: A20 keeps a
   tenant-scoped log-fold fallback behind the projection read (read-after-write); A23 also serializes the
   SQLite writer with a mutex and applies pragmas pool-wide so the atomic wrap is actually safe under
   concurrency; A24 enforces read-only + an ITK_SQL_CONNECTOR_DIR allowlist; A28 uses a keyed remount and
   folded in an incidental null-deref fix (a policy's versions can be null before first publish). New
   tests accompany the riskier changes (delivery bounding/stop, full-queue async, KMS integrity, UpdateDoc
   atomicity, DSN hardening, BPMN collisions, SCIM filter, policy-switch e2e).
6. **Decision-table hit policies + aggregators** (A32–A33) — extend the decision-table node beyond
   first/all to the standard set (UNIQUE with conflict detection, ANY, FIRST, RULE ORDER, COLLECT with
   SUM/MIN/MAX/COUNT), surfaced in the builder + OpenAPI + assertions; and document the expression language
   (expr-lang + Starlark, per D9) as a stable contract — explicitly not adding a second expression engine.
   **Shipped in PR #11:** the `decision_table` config gained `hit` (first|unique|any|rule_order|collect) +
   `aggregate` (sum|min|max|count for collect); UNIQUE/ANY fail loudly on conflicts, rule_order/collect
   gather per-target values with rules independent, and `mode:"all"` stays back-compatible. Builder
   hit-policy/aggregate selects + OpenAPI prose updated; assertions unchanged (they match the output map).
   A33 landed as docs/EXPRESSIONS.md (linked from the engine README + OpenAPI). Domain hit-policy tests +
   a builder e2e added.
7. **External decision API (compatibility surface)** (A34–A37) — a neutral-named, versioned compatibility
   API faithful to the comparable platform's documented API to the extent legally possible: an
   array-of-rows decide endpoint, per-flow generated OpenAPI/Swagger, API-key pattern/wildcard scoping, and
   decision-history query params (time range + include-node-results). Only functional API shapes are
   reimplemented (interoperability); no docs/branding copied; the competitor is never named in-repo.
   Prereq: confirm the live contract from a legitimate account (its current docs are auth-gated).
   **Shipped in PR #12:** the live contract was NOT fetched (auth-gated) — by decision we implemented the
   neutral functional shapes. The array-of-rows decide endpoint already existed (`/decide/batch`); the
   missing piece, a per-flow generated OpenAPI 3.1 contract, landed at `GET /v1/flows/{slug}/openapi.json`
   (embeds the flow's published input schema). API-key `Scope` gained a `*` wildcard + `/*` prefix patterns
   AND the first real enforcement on the decide/batch/preapprove endpoints (403 on a non-matching env; dev +
   test keys set to `*` to preserve behaviour). `/v1/decisions` gained `start_time`/`end_time` (since/until
   aliases) and `include_node_results`. Tests: ForFlow generator, per-flow endpoint e2e, scope Allows/Valid
   unit + decide-enforcement e2e.
8. **ML model hosting** (A38–A41, epic — needs a product decision) — the one sizeable in-scope feature
   gap: hosting/serving predictive models alongside rules (a predict node + model registry + monitoring).
   Larger than one PR and bounded by the §9 "ONNX serving at scale" non-goal — scope before building.
   Connector breadth, an authoring AI-copilot, and a gRPC/Arrow batch path ride here as stretch.
   **Shipped in PR #15 (the models-as-data slice):** `decision-engine/models` hosts models as data and
   evaluates them in a pure, deterministic function — three kinds in one PR (logistic regression, a gbm
   tree-ensemble, an expr-lang scoring expression), no external runtime (the non-goal stands). A model
   registry (`POST/GET /v1/models`, command→`ModelDefined`→projection→`ModelView`) + a **Predict node**:
   the shell resolves the model from the registry, evaluates it over the input, and injects
   `predict.<output>` (recorded for replay — mirrors Connect/AI). Builder gained a `/models` registry page
   and a predict node panel; OpenAPI + the engine README updated. **Follow-ups:** BYO external model
   serving (the "both, phased" second half), and the A39–A41 stretch items (connector breadth, authoring
   AI-copilot, gRPC/Arrow batch) remain open. Bespoke model monitoring (drift) rides on the existing
   decision history/analytics for now.
9. **Persona-adaptive UI + API-first** (A42–A45, epic — a key differentiator) — make personas
   **meaningful adaptations**, not skins. Today Builder/Operator/Showcase only swap accent/type/density
   over one layout; instead each persona gets a distinct default landing, surfaced primary actions,
   terminology, density, and emphasized data. Expand to a neatly-defined, config-driven, extensible set
   covering the platform's real roles — proposed: **Workflow Designer** (visual flow authoring),
   **Developer/Integrator** (API explorer, keys, webhooks, decision-trace debugging), **Risk Operator**
   (queues, SLAs, monitors, case review), **Team Manager** (approvals, reviewer workload, SLA health),
   **Product/Experimentation** (A/B, shadow, policy impact, backtests), **Executive/Director** (KPIs,
   trends, governance posture), **Evaluator/Guest** (guided tour + sandbox for prospects / sales demos).
   A persona is a composition over the API, not a fork. This depends on and reinforces an **API-first
   guarantee**: every UI action is performed through the documented public API (no UI-only backdoors),
   so the UI is flexible and adaptable and external/embedded UIs are first-class — which also underpins
   PR7 (external API compatibility). Sizeable; likely several PRs (the persona model + API-first audit
   first, then the per-persona views).
   **Shipped in PR #13 (the model + API-first slice):** personas are now config-driven compositions
   (`lib/persona.ts`) — each of 7 real roles declares its own navigation (ordered, relabelled subset of
   the shared catalog), default home, and surfaced primary actions, so nav/landing/terminology/density all
   adapt per role (no longer a skin). The 3 original archetypes keep bespoke decks; the 4 new role personas
   use a config-driven `PersonaHome`. The **API-first guarantee** is documented (`docs/API-FIRST.md`) and
   enforced by `web/src/lib/api-first.test.ts` (the audit confirmed it already held: all calls go through
   `api.ts`/`/v1`, no server routes, only persona+theme are local).
   **Shipped in PR #14 (the deep per-persona views, A44):** Developer gained a real **API-keys management
   page** (`/keys` — list/create/rotate/revoke via `/v1/api-keys`, secret revealed once) plus traces /
   API-reference links; Executive's ShowcaseDeck gained a **decision-volume trend** (`decisionsByDay`) + a
   **governance tile** (live flows / pending four-eyes); Evaluator gained a **guided 4-step tour**
   (`EvaluatorTour`) over the live sandbox; Risk Operator keeps its OperatorDeck. The API reference is
   linked (server `/docs` + per-flow `openapi.json`), not embedded. Both persona slices are now done.

> Per project convention: at the **end of every phase**, update `PLAN.md` and `BUGS.md` in the same
> PR as the phase's code.

**Launch batches (post-audit, A1–A45 all merged).** With the round-3 backlog cleared, large
"launch" PRs bundled the remaining stretch + hardening (deliberately fat, to conserve review/CI cycles
into launch). **Batch 1 (PR #16):** ML phase 2 (BYO `external` served models), connector-catalog breadth,
the large-job NDJSON `…/decide/stream`, model-drift PSI monitoring, the authoring AI-copilot
(explain/suggest), and the A16/A17 visual polish. **Batch 2 (PR #17):** deeper copilot (server-validated
graph **generation** applied to the canvas), **windowed/time-bucketed drift** + a per-model PSI monitor
threshold, two **real connector adapters** (`graphql`, `static`) + more catalog templates, and a
**launch-readiness sweep** (8 MiB JSON body cap, `GET /version`, `docs/LAUNCH.md`). **Batch 3 (PR #18):**
the **scheduled model-drift push** (a `models.Scheduler` that sweeps every tenant's models on the
`INTRAKTIBLE_MONITOR_INTERVAL` cadence and pushes the ok→firing PSI edge to webhooks, deduped via
drift-`Alerted`/`Resolved` events — mirroring the flow monitor; `INTRAKTIBLE_MODEL_DRIFT_WINDOW`
narrows the firing window); **first-class provider connectors** (`http`/`graphql` gain an `auth`
block — bearer/header/basic/query — + custom headers; `plaid` and `stripe` adapters with sealed
credentials) plus **define-time connector-config validation** (a bad endpoint/credential now fails
on save, not on first fetch); and a **launch dry-run against Postgres** (`--store/--log=postgres`,
full LAUNCH.md walk + the `INTRAKTIBLE_TEST_POSTGRES` contract tests green). The round-3 ideas are
now all delivered.

**Type-safety & correctness-by-design sweep (post-launch, PRs #19–#21).** An audit-driven pass
(tracked as TS1–TS9 in BUGS.md): a `platform/mo` `Option[T]`/`Result[T]` foundation; real bug fixes
(GBM nil-deref, projector error-swallow, SSE/WS stream hang, bulk-case abort, builder version
seeding, several web guards); a decide error taxonomy (`ErrBadRequest`/`ErrNotFound` → HTTP
cause→status); **publish-time flow validation** (`domain.ValidateFlow` dry-compiles every node's
config + expressions so a broken flow can't deploy to prod) + a canonical etag; a type-enforced
projection store contract (`store.Ephemeral` vs `TxStore`, so a durable non-transactional store
can't silently double-count); named enum types (`ModelKind`, `CaseStatus`, `RunStatus`,
`ConnectorType`, `Environment`, `Disposition`); and `identity.New` at the SSO mint boundary. The
deliberately-skipped maximal items (codebase-wide field unexporting, wholesale `Option`/`Result`
conversion, the replay-breaking typed `PreResolved` seam) are recorded with rationale in TS9.

**Bug + type-strengthening + fuzz sweeps (post-launch, audit-driven, BF1–BF32 / TS13–TS17 in
BUGS.md).** Three fan-out audit rounds, each landed as one fat PR through the full strict gate. The
first two (BF1–BF16) fixed a drift-projector poison-event crash, an OAuth2 token-cache key bug, a
notify total-failure dedup-into-silence (and the scheduler-sweep-starvation regression that fix
introduced), a SCIM omitted-`active` lockout, BPMN id collisions, and an erasure envelope-shape
confusion; added native Go fuzz targets across the parse/validate boundaries; and purged a stray
52 MB binary from all git history. **Round 3** closed three criticals — a **privilege escalation**
where the API-key→session login dropped the key's scope so a sandbox key could reach production
(fixed by carrying scope into the session and collapsing role+scope into one `httpx.Principal`, so
role-without-scope is unrepresentable; the env gate now fails closed); the **dev admin key seeded
by default** (now in-memory-store only, so a durable/production deploy can never boot with it); and
**permanent webhook failures retried forever** (a `notify.Outcome` enum makes the retry/dedup
decision total). Plus a scope/role **ceiling** on key minting/rotation, SCIM body-size limits, a
batch infra-error misclassification, agent tool-loop cancellation, a web sibling-nav load race, an
exhaustive `NodeType` union, and an array of MED/LOW correctness fixes (policy latest-version
selection, NaN-threshold rejection, null-vs-absent assertions, average rounding). Two new fuzz
targets (Mermaid export, SCIM filter/patch) found no crashers. **Round 4** (BF33–BF41 / TS18–TS20)
fixed a CRITICAL panic in the pure core (a Decision Table with hit:`any` and zero matching rows
resliced an empty slice — crashing the decide path and any backtest/shadow batch), propagated the
round-3 stale-load guard to the remaining detail pages (decisions/cases/entities/agents +
CommentThread), made comment/notification ordering stable (a monotonic `Seq` tiebreak), and pushed
the tenant key prefix into `Store.List` as an indexed range scan so a listing no longer loads every
tenant's rows to filter in Go. Type-strengthening: a `ParseStatus` parse-don't-validate boundary in
the case projector (an unknown status can't enter the read model), publish-time validation of the
Decision Table hit-policy/aggregate (a typo fails at publish, not the first decision), and the
prefix in the `Store.List` signature. Plus the AI provider checking status before decoding (clean
errors on a 502), a WS-stream cancel-on-disconnect (parity with SSE), an api-key revoke scope
ceiling, and a feature-window boundary fix. New fuzz target `FuzzPrefixUpperBound` validates the
store range invariant. The Postgres multi-node delivery seq-gap (BIGSERIAL commit-vs-seq order) was
verified real and documented with the fix (a visibility-horizon gate), deliberately not shipped
untested as it needs a live multi-node Postgres and a naive watermark would deadlock on burned
sequence numbers. **Round 5** (BF42–BF49 / TS21–TS22) caught a **CRITICAL regression from round 4**:
the new `Store.List` prefix range scan (`key < prefixUpperBound`, a byte-successor bound) is correct
only under byte ordering, but the Postgres `docs.key` column inherits the database's default —
usually *linguistic* — collation, so on a stock Postgres the range silently dropped rows (empty
notifications inbox / comment threads). Fixed by pinning every key comparison + `ORDER BY` to
`COLLATE "C"` and adding a C-collation index (and a live-DB prefix test + a full-contract fuzz).
Also: an agent-tool-loop **capability-confinement bypass** (the loop executed any tool name the
provider returned, not just the agent's declared set — a prompt-injected model could reach any
connector in the tenant; now gated on the resolved-tool allowlist), a **crypto-shred hole** where
the manual-review escalation recorded `company_name`/`case_type` unsealed (now derived from the
sealed node output), the BF24 stale-load guard propagated to the test-run telemetry painter, and a
cluster of web interaction races/UX bugs (bulk-failure toast, login double-submit, pager overshoot,
drift-window request token). Security hardening: the SQLite-connector directory confinement now
resolves symlinks before the containment check, and `httpx.Download` sanitizes the
Content-Disposition filename. New fuzz target `FuzzMemoryListPrefix` validates the store prefix
contract end-to-end.

**Gate stability + type/test/persona follow-ups (post-round-5).** First, the recurring pre-push
Playwright flakiness (which forced `--no-verify` on three PRs) was root-caused to three compounding
modes — a `BlockingIOError` from the verbose reporter overflowing pre-commit's non-blocking pipe, a
`go run` orphan leaving a stale server the next run silently reused, and a persisted event log
accumulating flows past the engine list's render cap — and fixed (dot reporter + HTML to disk, a
built-binary backend that wipes its log with `reuseExistingServer:false`, and retries) so the gate
passes without a bypass (E1). Then three deferred follow-ups landed in one PR: (1) **web
discriminated unions** (TS23) — the bare-string `status`/`disposition`/`op`/`kind` fields in api.ts
became string-literal unions mirroring the Go enums, so a typo'd value fails svelte-check rather
than rendering an unstyled badge; (2) **`testing/synctest`** (E2) — the eventlog delivery
concurrency tests now run in a fake-time bubble with deterministic quiescence detection (no
timeouts, no rendezvous race); and (3) **persona depth** (P1) — a typed `PersonaLens` gives each
persona a default focus on the shared list surfaces (operator → open case queue, developer → failed
traces), data re-prioritisation over the one API rather than a skin.

**Persona lens extension + sweep round 6 (P2 / BF50–BF54).** The persona lens deepened: the
decisions lens is now multi-axis (status + variant + env), the product persona lands on the
challenger experiment arm, and the decisions page gained the variant filter control the API already
supported. Extending into that surface surfaced a **regression from the previous PR** — `TS23` had
narrowed `Decision.status` to `'completed'|'failed'`, but an in-flight decision is `'started'`, so
the union denied a real wire value and rendered an unstyled badge; fixed with a `DecisionStatus`
union (BF50). The sweep also fixed: the agent tool-loop echoing un-normalized tool-call IDs that
break assistant↔result correlation on strict providers (BF51), `flattenQuery` silently dropping
array/object query params instead of failing loudly (BF52), a SCIM `userName eq ""` filter
enumerating the whole tenant instead of matching none (BF53), and SCIM list determinism + an
all-permanent-delivery sweep inflating the `Delivered` metric (BF54). Verified sound: the persona
lens (no SSR/localStorage issue), the BF42 COLLATE "C" range, the synctest conversion, and the
decide hot path. Two refactor-of-working-code type-strengthenings (a typed notify `DeliverySummary`,
a Go `RunStatus` named type) are recorded as deferred follow-ups.

**Type-strengthening + persona depth + sweep round 7 (TS24–TS25 / P3 / BF55–BF61).** The two deferred
type-strengthenings landed: `notify.Deliver` returns a typed `DeliverySummary` with `RetryWorthy()`/
`Delivered()` predicates (retiring the error-as-control-flow), and `domain.RunStatus` is now a named
type with `Valid()` (mirroring `CaseStatus`). Persona depth gained an *order* dimension — the operator's
case queue is now sorted by SLA urgency (soonest-due first), complementary to the filter lens. The sweep
(two fan-out audits) fixed a **CRITICAL**: a node/edge deleted on the builder canvas was removed from the
view but not the publish model, so it reappeared and got **re-published** (now reconciled via an
`ondelete` handler). Also: the `predict` node rendered as a generic task (not a service call) in all
exporters; a feature-window field that silently accepted empty→0; a decisions-list load race; a
backtest-dataset array guard; WAL trusting a stored seq over the authoritative byte-offset; and a
feature-aggregation precondition. Documented-not-changed (deliberate): the `requiredRole` operator
default (a policy, all routes classified), verbose 5xx (the self-hosted fail-loudly stance), and the
CommandPalette focus-trap (UI-polish backlog).

**Type-strengthening + persona depth + sweep + fuzzing round 8 (TS26–TS32 / BF62–BF74 / P4–P6 /
FUZZ).** Type-strengthening continued making bad states unrepresentable: feature `Aggregation`,
pre-approval `Status`, notification `Kind`, the decision-table `hitPolicy`, and a CaseStatus
`Terminal()/CanTransitionTo()` state machine became named types; the agent-run projector now
parse-guards its status at the decode boundary; and on the web the production four-eyes gate is now
type-checked via a real `Environment` union (plus a real `assertNever`, `EditNode.type → NodeType`,
monitor metric/op unions, and lens-typed filter state). The sweep fixed a **HIGH**: a completed case
could be reopened, silently re-arming the SLA sweep — now blocked by the CaseStatus transition guard.
Also: non-deterministic decision-history pagination (unstable sort, no tiebreaker), a feature-sum
overflow to a non-finite value, `DecodeJSON` accepting trailing data, non-constant-time CSRF-token
compares, a dropped model baseline, and a batch of web async/interaction bugs (cases/data/audit load
races, an API-key rotate double-submit that lost a minted secret, a blob-URL download race, cleared
number inputs posting null, a NaN drift histogram). Persona depth went beyond the lens: the
PersonaHome tiles are now persona-chosen stats over the shared data (a manager's pending/overdue vs a
developer's failed/latency), the decisions table leads with the columns each role debugs by, and the
empty states speak the role's own vocabulary. Five native fuzz harnesses were added at the
highest-value un-fuzzed boundaries (the runtime `Execute` path, the WAL on-disk decoder, AES-GCM
secret round-trip, feature aggregation — which surfaced the overflow bug — and the DOT exporter).
Two pre-existing dead functions were removed to keep the deadcode gate green (dupl 9→8, none added).

**Structural type-strengthening + Go↔TS enum codegen + sweep + fuzzing round 9 (TS33–TS36 /
BF75–BF86 / FUZZ).** This round targeted the structural ("two fields must agree") and drift classes.
Auth-key usability became a single `KeyStatus`/`Usable(now)` predicate (was open-coded three ways);
the GBM `Link` became a typed `GBMLink` (a mistyped link no longer silently drops the sigmoid); the
agent `Outcome` now enforces its Status⇄payload invariant at the package boundary. The biggest piece:
the TS string-literal unions that mirror Go enums are now **generated from the Go consts**
(`cmd/tsenums` → `enums.generated.ts`, `make tsenums`) with a Go drift-check test failing the gate if
they fall out of sync — Go↔TS enum drift is now unrepresentable. The sweep fixed a **HIGH security**
bug — a caller could forge the engine-reserved `features`/`predict`/… input namespaces when a flow had
no such node, reading as trusted engine-resolved data (now stripped before injection) — plus a
telemetry-repaint-on-edit race, duplicate-output collisions caught at publish, a non-tenant-scoped
OAuth token cache, an empty-value connector-auth footgun, the rotate API-key role ceiling, a
decision-table aggregate that could go non-finite/order-dependent, and a handful of web bugs
(challenger-% clamp, stale backtest report, duplicate each-keys, blob-URL download race). Four more
fuzz harnesses landed, including one over the security-critical SQLite DSN path parser (asserting
read-only + directory containment) and one that surfaced the aggregate-overflow fix.

**Round-10 deep follow-ups, folded into the same PR (H1/H2/M5 + low bugs + transposition types).** The
three items previously documented-not-fixed were implemented: the cross-process version/slug **TOCTOU**
is closed by a storage-layer optimistic-concurrency claim (`Envelope.Unique` enforced as a uniqueness
constraint across WAL/SQLite/Postgres/NATS, with the decision-engine retrying on `ErrConflict`); the
**NATS** consumer now re-subscribes from the last delivered seq on reconnect; and **SCIM** gained
list pagination, externalId-idempotent create, and Okta path-less group membership. Plus a batch of
latent fixes (a Postgres `FOR UPDATE` row lock in `UpdateDoc`, a Recover double-write guard for
streaming, comment parent validation, an SLA-days bound, a SQLite seq guard) and transposition-proofing
(`DecideResult`/disposition strong types, a comments `Subject` struct, monitor disposition consts), and a
new SSE-parser fuzz harness. Two pure cross-package signature refactors (EntityRef branding, the SCIM
`identity.Identity` threading) and the authz `r.Pattern` restructure were consciously left as fast
follow-ups — they prevent only theoretical transpositions with trusted callers and carry large churn /
auth-regression surface for no behavioral change (detail in BUGS.md).

**Review sweep — bugs + UX/a11y + persona dead-ends (one fat PR).** A cross-cutting review (Go bug sweep,
web UX/a11y sweep, persona-flow coherence, plus light/dark screenshots of every route) turned up and fixed:
(bugs) the encrypting event-log's `Subscribe` silently corrupted on an undecryptable payload (forwarded a
sealed envelope that projectors decoded to a zero-value struct and checkpointed) — now substitutes a
non-decodable sentinel so the projector errors and /healthz degrades; and per-flow grants were enforced on
only four of the documented change-control actions — added `grants.AllowedAny` + enforcement to
publish/approve/reject/cancel-schedule/request-deployment. (UX) destructive actions (rollback, cancel
schedule, revoke grant, clear SLO) now confirm + toast; the SLO editor validates input + has a
non-destructive edit path; the agents page got success toasts + an escalate confirm; pass/fail glyphs got
aria-labels. (persona) `/v1/me` now returns role so the nav hides admin-only items (mrm, audit) for
non-admins and `/mrm` renders a graceful forbidden state (no more 403 dead-end for manager/executive
personas); the evaluator nav gained `cases` (its tour routed there) and the builder deck's dead `/hello`
link became a real "context data" link. The screenshot design-review helper was extended to cover the new
pages and seed richer fixtures. Tests added for the log halt, grant enforcement, role-aware nav.

**Model risk management packaging (SR 11-7 / SS1/23).** A new read-only `mrm` package aggregates the
inventory + validation evidence + monitoring that already exists across the platform into one regulated
artifact (`GET /v1/mrm/report`, admin-gated). It inventories every "model" — a decision flow, a predictive
model, and an AI agent — with version + owner (last publisher) + deployments; validation evidence (a
flow's assertion suite run live + shadow divergence; an agent's eval cases; a predictive model's drift
baseline) classified tested/failing/none; and monitoring (decisions, success rate, firing monitors, PSI
drift, SLO attainment). It flags governance gaps per model (unvalidated, failing assertions, firing
monitor, breaching SLO, drifting), and exports as JSON / CSV / Markdown (the filed document). No new
events or write side — a pure aggregation over the existing read models (it only runs the pure assertion
suite). Surfaced as a **Model risk** UI page (manager/showcase nav). Tests cover the aggregation, the
issue flags, validation coverage, and the CSV/Markdown export (incl. formula-injection neutralization).

**AI/ML governance: agent registry/versioning + offline eval.** An agent's definition (model + system
prompt + schema + tools) is now an immutably **versioned** registry entry: each define appends a
content-etag'd version to the agent's history (idempotent on an unchanged redefine), mirroring the
flow-version registry — `applyDefined` appends instead of overwriting, `AgentView` gains `Latest` +
`Versions[]`, and `agents.ReadConfig(version)` resolves any past version. Runs can **pin a version**
(`POST /v1/agents/{name}/run {version}`); `GET …/versions` lists history. An **offline eval harness**
(new `agent-manager/eval`) stores golden cases per agent (event-sourced: prompt + a `contains`/`equals`/
`json_subset` expectation) and runs them on demand through the provider against a chosen version, scored
pass/fail — **recording nothing** (the assertions/backtest model), so evaluating a non-deterministic,
billable model never pollutes the run log. Refactored `InvokeWithTools` to expose an `InvokeConfig` seam
(run an explicit config, no registry lookup) that both version-pinned runs and the eval harness use. UI:
version-history + an eval panel on the agent page; full typed SDK in api.ts. Tests across scoring modes,
the run harness (deterministic via the Stub), and versioning/idempotence.

**Polish bundle: AI guardrails + alerting + rollback/scheduled deploys + per-flow permissions.** Four
P1/P2 polish items in one PR. (1) **AI guardrails** (`ai.Guard` decorator over every registered provider,
covering the Agent Manager and the Copilot): per-provider token-bucket rate limit, free-text PII
redaction (prompt + output) + structured-field redaction, and jailbreak/prompt-injection blocking — all
env-configured (`INTRAKTIBLE_AI_RATE_LIMIT_RPS`/`_BURST`, `INTRAKTIBLE_AI_GUARDRAIL_*`), off by default.
(2) **Alerting polish**: PSI + KL distribution-drift as first-class monitor metrics (enum-driven, so they
flow through codegen + the UI dropdown), and per-webhook message **templates** + event **routing** filters
(the notifier renders/filters per webhook). (3) **Instant rollback** (revert an env to its prior live
version, folded from the deploy event stream, audited as `version_rolled_back`) + **scheduled / time-boxed
deploys** (`decision-engine/schedule`: an event-sourced schedule projection + a deploy scheduler that
activates due deploys and auto-reverts expired time-boxed ones, sharing the monitor cadence). (4)
**Fine-grained per-flow/per-environment permissions** (`decision-engine/grants`): opt-in, event-sourced
grants that, when present on a flow, additionally gate deploy/rollback/schedule/promote on a per-env grant
(admins always pass; a flow with no grants is unchanged). UI: drift PSI/KL, webhook template/events,
a rollback button, and scheduled-deploy + access-grant panels in the builder. Tests across every layer.

**Encryption at rest + secrets management.** The last open P0 landed as one PR. The AES-256-GCM
seal/open construction — previously duplicated in the connector-credential sealer and the crypto-shred
erasure vault — is extracted into a shared `platform/secretbox` primitive: `AESGCMSecretBox`, a rotating
`Keyring` (fingerprint-derived key ids, primary seals / any retained key opens), the versioned JSON
`Envelope`, byte-level `Seal`/`Open`/`IsSealed`, and `DecodeKey`/`KeyringFromKeys` env helpers. Connectors
now alias secretbox (wire-identical envelope, so already-sealed connector events still replay) and erasure
delegates its crypto to it (byte-identical, so already-shredded data still opens). On top of that,
**encryption at rest**: with `INTRAKTIBLE_ENCRYPTION_KEY` set (`_KEYS_PREVIOUS` for zero-downtime rotation),
transparent decorators `eventlog.Encrypted` and `store.Encrypted` seal event-log **payloads** and
projection-store **documents** (covering the session + managed-API-key stores, which share the store).
Only values are sealed — event metadata + the optimistic-concurrency claim, and store collection names +
keys, stay plaintext, so ordering, the uniqueness constraint, audit metadata filters, and key-range
scans/indexes are untouched. The sealed form is a recognizable JSON envelope, so reads open sealed values
and pass plaintext through: enabling encryption needs **no migration pass**. Connector credentials keep the
external-KMS (AWS/GCP) option. Off by default; the at-rest key is operator-supplied. Scope note: a
KMS-wrapped DEK for the hot path is a follow-up (KMS-per-op would add a round-trip to every store op). Tests
cover the primitive (crypto, rotation, envelope, key decode), both decorators (seal-at-rest, metadata
plaintext, transparent migration, the Tx read-modify-write path), and no-regression on connectors/erasure.

**Observability: tracing + SLOs + AI cost.** A full observability slice landed as one PR. (1)
**Distributed tracing** via OpenTelemetry: a new `platform/telemetry` package owns the TracerProvider,
off by default and configured by env (`INTRAKTIBLE_OTEL_EXPORTER=stdout|otlp`, `OTEL_EXPORTER_OTLP_*`,
`INTRAKTIBLE_OTEL_SAMPLE_RATIO`); an `httpx.Tracing` middleware spans every request named by the matched
route template (continuing propagated W3C trace-context, tagging the request id); and the decide path is
spanned end to end — the decision, each external hop (features/connector/AI/model), and every node. The
per-node spans flow through a *pure* `domain.NodeObserver` interface (`ExecuteObserved`) the shell
implements, so the deterministic core imports no telemetry. (2) **Per-flow SLOs**: `GET/PUT
/v1/flows/{id}/slo` records an availability + latency objective as a `decision.flow.slo_set` event folded
onto the flow read model; attainment (success rate vs target, error-budget burn, avg-latency vs target)
is computed on read from the live decision metrics (`analytics.Attainment`). (3) **AI cost**: the AI
provider captures token usage (`usage` object + `stream_options.include_usage` for streaming) on
`ai.Response`, accumulated across the tool-calling loop and recorded on `AgentRunRecorded`
(`prompt_tokens`/`completion_tokens`, omitempty → replay-stable); an operator price table
(`INTRAKTIBLE_AI_PRICES`) derives per-model/total cost in the run summary. A new **Observability** UI page
(nav-integrated per persona) shows the AI usage/cost roll-up and per-flow SLO attainment, with an inline
set/clear-objective editor (`SloCard.svelte`). Honest scope: SLO attainment is over the cumulative
metrics (all-time), not a rolling window (windowed SLOs need time-bucketed metrics — follow-up).

**Transposition-prevention refactors (TS40–TS42).** The three follow-ups left from round 10 landed as
one PR: the decision-subject (entity type, id) is now the shared branded `platform/entity.Ref` threaded
through the feature/pre-approval ports (a swapped pair fails to compile rather than silently keying the
wrong entity); the SCIM Store's ~14 methods take `identity.Identity` instead of a transposable
`(org, workspace)` string pair (cross-tenant safety); and `Authorize` classifies by the matched route
template (`AuthorizeRoutes` via `mux.Handler`) rather than raw-path substrings, so no user-controlled
path segment can influence the role decision. All wire-identical, behavior-preserving; tests added.

**Variant + DeploymentRequestStatus named types (TS37).** The two enums still carried as scattered
string literals — the A/B `Variant` (champion|challenger) and the maker-checker
`DeploymentRequestStatus` (pending|approved|rejected) — became named types (`domain.Variant`,
`flows.RequestStatus`) wired through the decide path, analytics, the deployment-request projection,
the command-side fold, and the service responses, with `Valid()` methods. `cmd/tsenums` now generates
both, so all 12 Go↔TS enums flow from the single Go source through the codegen + drift check (api.ts
no longer hand-defines any 1:1 enum).

**Bug sweep + type-strengthening + fuzzing round 10 (BF87–BF95 / TS38–TS39 / FUZZ).** Four parallel
sweep agents over the less-trodden code. Security fixes: a too-loose sealed-envelope check that could
write a credential-shaped object to the log in the clear (now requires the exact envelope shape), and
a defense-in-depth tightening of the audit authz gate. Correctness: a determinism tiebreaker for the
webhook/monitor list ordering (folded into a shared generic `store.SortByTime`/`ListByTime` now used
by history, notify, and monitor), a WAL short-read guard, an OAuth token-cache clock fix, and four web
fixes (a blob-URL download race, an SSO-discovery rejection, a silent agent-stream error, a
notification label). Type-strengthening extended the Go→TS codegen to NodeType/Aggregation/Role/Scope
(16 generated unions; the keys page's hand-duplicated ROLES/SCOPES are gone), typed the monitor
command + connector-catalog type, and four more `api.ts` fields. Four fuzz harnesses landed at
security/recursion boundaries — PII-redaction completeness, crypto-shred round-trip, the Starlark
conversion fixpoint, and CSV-injection neutralization. Documented-not-fixed (deliberate, scoped as
follow-ups): the cross-process version/slug TOCTOU (needs a storage-layer compare-and-set; the
monolith is correct), NATS live-delivery catch-up across reconnect, and SCIM list pagination.

**Public demo + base-path portability + UI/UX review** (done). The SvelteKit app is now base-path
portable (`paths.base` from `BASE_PATH`; internal links via a `$lib/paths` `appHref` that no-ops at
the embedded root; fonts moved into the source tree so Vite base-rewrites them), enabling a fully
interactive, **backend-less build that runs on GitHub Pages at `/demo/`**. A `src/lib/demo/` mock
backend (in-memory store typed against `api.ts`, a safe JS flow interpreter, a `/v1` router) is
installed only via a `VITE_DEMO`-guarded dynamic import, so it is dead-code-eliminated from the
binary. A `pages` workflow builds + smoke-tests the demo and assembles a one-artifact site — demo at
`/demo/`, a versioned root landing reserving `/` for a future presentation page, and a `/docs/`
placeholder (a rendered docs site + Storybook deferred). A UI/UX screenshot review across personas
confirmed the app is sound; the one fix was reconciling a demo-seed run-count inconsistency.

A follow-up substantially enriched the demo (6 multi-node flows, ~44 decisions, 14 cases, plus
agents/models/context/policies/audit) and added a cast of demo **users with RBAC roles** and a
demo-only identity switcher, so a visitor can view the app AS any role and watch role-gated surfaces
change live. That re-review surfaced one real app bug — the persona HOME wasn't role-gating its
workspace chips / primary actions like the header nav did — fixed with a new
`persona.actionsFor(p, role)` and threading the signed-in role into PersonaHome.

A further pass made the demo genuinely **playable**: state now persists to localStorage and
accumulates across reloads (with a Reset control), every write is attributed to the switched demo
user (not a hardcoded actor) and lands in the audit log, and **maker-checker** is enforced (approver
role + no self-approval) — so a visitor can drive a flow build → deploy → decide → triage → resolve
end-to-end. A multi-agent UX review (interactivity, persona/role, a11y/layout) drove a batch of fixes:
role-gated write controls (new `$lib/roles`), admin-gated API keys, a full-catalog role-gated command
palette, DemoBanner contrast (`var(--on-accent)`), table-overflow + skeleton + empty-state cleanups.

A third review (a 6-agent fleet incl. screenshot-driven vision agents) added an **in-app page guide**
— a route-keyed slide-over explaining each page's purpose, capabilities, and key flows from one
content registry (`$lib/help`) — and de-anemicised the real shared components: a shared `Badge`,
a PersonaHome **cockpit** (KPI cards + recent-activity), a decision **verdict hero**, list-page KPIs,
agent run output, MRM rendering its previously-discarded governance signals, and the long-broken mobile
banner. Implementation fanned out across isolated git worktrees and integrated on the shared Badge.

Documentation followed: a `docs/JOURNEYS.md` documenting the end-to-end user journeys, a **rendered
`/docs/` site** generated from the Markdown at deploy time (`web/scripts/build-docs.mjs` via `marked`,
themed with a sidebar) and a real **landing page** at the Pages root — all three assembled by the
`pages` workflow alongside the demo.

A faithfulness audit (three agents: journeys-vs-backend, fake-hunt, live walkthrough) confirmed the
real product is substantial — not smoke-and-mirrors — and closed the specific gaps it found. The
backend gained a **record-free preview decide** (`preview: true`) and a real **decision→case link**
(`case_id` on the decision read-model), plus a default review SLA, a `MANUAL_REVIEW` reason code, and a
server-side `agent_review` default. The demo was made faithful to it: agent output is a real reply
(structured verdict or narrative) rather than a `stub:` echo, the streaming run no longer dead-ends,
admin-only surfaces gate server-side, and Copilot describes the actual graph.

A follow-up closed the remaining demo-representation gaps and surfaced the preview in the UI: the demo
decide path now honours pre-approvals (short-circuit + honored count), interprets the connect node,
and computes a real PSI per model; the builder's Test tab exposes a no-record **Preview** toggle (off
by default, so an ordinary test still records an inspectable sandbox decision).

A four-agent review (UI/UX, user-flows, QA, product) then drove an **execution-legibility** pass: the
product was substantial but the engine was invisible and the docs/guide layer thin. A new
**`docs/EXECUTION.md` — "How a decision runs"** documents the node-graph execution model end to end
(and how the in-browser demo mirrors the real Go engine), rendered into `/docs/`. **Discreet inline
docs** (a new `Hint` component) explain reason codes, the node trace, and what a test run executes; the
trace always surfaces reason codes; and the demo banner now says a real engine runs in the browser. The
same pass unified the core surface's name to **Flows**, added a **disposition column** to the decisions
list, made "Sample input" route a real branch, and cleared a batch of UX/QA nits.

To make the personas a true deep adaptation rather than skins, the developer/manager/product homes —
which previously shared one generic "recent decisions" feed — each gained a **role-specific panel**
(config-driven via `persona.homePanel`): manager → pending four-eyes approvals, developer → failing
traces, product → champion-vs-challenger.

A second four-agent review then cleared the next layer: a real **active-nav/`aria-current` bug** under
the base path, **per-page `<title>`s**, naming-register drift (Cases/Agents/Traces H1s), four-eyes
self-approval disabled in the UI, deploy **deep-linking** (`?tab=deploy`), a schema-prefilled builder
test input, the seed's reverse decision→case link, agent-escalation context + an "open case" link,
create-flow validation, and a batch of a11y/polish (Hint viewport clamp, table scroll-shadow, landing
deep-links to proof).

---

## 9. MVP non-goals
Full SSO, multi-tenant billing, the 200+ real data connectors, ONNX model serving at scale,
production HA/clustering, and exact API/UX parity with any commercial product. These are post-MVP.

## 10. Open questions (to resolve as we go)
1. **Log storage:** BadgerDB vs a custom segmented WAL — benchmark in Phase 0 (only remaining
   backbone decision; interface is fixed either way).
2. **Code node language:** Starlark (Python-like, safe) for MVP; possibly add JS (Goja) or WASM for
   user code later.

_Resolved during requirements gathering:_ tech stack (Go + SvelteKit/Svelte Flow), event backbone
(pure-Go embedded log), ES purity (hybrid), build sequence (core→engine→cases→context→agents),
**multi-tenancy (org+workspace from day 1)**, **web delivery (embedded in the Go binary)**,
**auth (API keys + minimal session login)**, AI (pluggable provider).
