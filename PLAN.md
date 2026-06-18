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
including cross-node delivery and sub-poll NOTIFY latency. NATS/Kafka backends remain.

**SSO / OIDC (post-MVP, enterprise identity).** `platform/auth.OIDCAuthenticator` + `platform/httpx`
`/v1/auth/oidc/{provider}/login|callback` add OIDC Authorization-Code SSO: the IdP's ID token is
verified against its JWKS (issuer/audience/expiry + nonce) via `coreos/go-oidc`, a state cookie covers
CSRF, and the verified email plus a configurable **groups-claim → role** mapping issues a normal session.
Providers are env-configured (`INTRAKTIBLE_OIDC_PROVIDERS`); **Google** and **AWS Cognito** ship with
sensible defaults. The login page renders a "Sign in with …" button per provider. SAML + SCIM remain.

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

> Per project convention: at the **end of every phase**, update `PLAN.md` and `BUGS.md` in the same
> PR as the phase's code.

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
