<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# intraktible — Implementation Plan

> Open-source MVPs of the four user-facing components of a commercial **Agentic Decision
> Platform**. Source of truth for scope and architecture; updated at the end of every phase
> (alongside `BUGS.md`). **New here? Start with [AGENTS.md](AGENTS.md).**

Research basis: the reverse-engineered understanding of the reference platform lives one level up
(`../products/`, `../specs/`, `../ENDPOINTS.md`, `../docs/`). `intraktible` is an
independent open-source reimplementation of the _concepts_, not a copy of any vendor's code or assets.

**License: `AGPL-3.0-or-later`** ([`LICENSE`](LICENSE), policy in [`docs/LICENSING.md`](docs/LICENSING.md)).
**Every dependency must be AGPL-compatible** — permissive (MIT/BSD/ISC/Apache-2.0/MPL-2.0) or
compatible copyleft (LGPL, GPL-2.0-or-later, GPL-3.0+, AGPL). **Disallowed:** SSPL, BUSL/BSL,
Elastic License, Commons Clause, GPL-2.0-_only_, and any proprietary/source-available license.
Enforced in CI via `go-licenses` (Go) and `license-checker` (web); a disallowed dep fails the build.
As a network service, **AGPL §13** applies — hosted instances expose a source offer (UI + `/source`).

---

## 1. The four components (each a subdirectory; MVPs)

| Dir                | Component           | One-liner                                                              |
| ------------------ | ------------------- | ---------------------------------------------------------------------- |
| `decision-engine/` | **Decision Engine** | Drag-and-drop builder + execution runtime for versioned decision flows |
| `case-manager/`    | **Case Manager**    | Queues + dashboards for human review of escalated decisions            |
| `context-layer/`   | **Context Layer**   | Entities/events/features data model + connectors (data marketplace)    |
| `agent-manager/`   | **Agent Manager**   | Configure/run/monitor task agents (LLM + tools) inside flows           |

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
  _view_), or compensating events.

### 3.3 Decision execution as a logged stream

Each `/decide` call becomes a **DecisionStarted** event; every node execution emits a
**NodeEvaluated** event (inputs, output, duration); completion emits **DecisionCompleted** (or
**DecisionFailed**). This _is_ the Decision History (replayable node-by-node), mirroring the
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

- `eventlog/` — append-only log + bus (§3.1). `projection/` — projection runtime + rebuild.
- `store/` — pluggable KV/JSONB store (SQLite, Postgres adapters). `schema/` — JSON Schema
  validation, dynamic types. `ai/` — provider interface + adapters (Claude/OpenAI/Gemini/Ollama).
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
  - license CI (`go-licenses`/`license-checker`); single Go module; `platform/eventlog` (pure-Go
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
  `features.txn_count_24h`). `DecisionStarted` retains the raw caller input while the separate
  `DecisionContextPrepared` event retains the authoritative feature/consent snapshot for replay and
  recovery. The engine stays free of a context-layer import via a `FeatureProvider` **port** (in
  `decision-engine/command`)
  satisfied by a `features.Provider` **adapter** wired at the composition root — preserving the
  build-order dependency direction. **Connectors** subsystem: a `Connect` interface + registry +
  reference connectors (an arbitrary-HTTP one and a deterministic `mock_bureau`); a definition is
  `{name, type, config}` and invoking a connector is an effect recorded as a `ConnectorFetched`
  event (the stored response, not a re-fetch, is what replay reads) — API `/v1/context/connectors`
  - `…/{name}/fetch` + `…/{name}/fetches`. A flow's **Connect node** is wired the same way as features:
    the pure interpreter yields it only when traversal reaches the node, and the
    shell invokes the connector with the exact current record, persists explicit
    requested/succeeded/failed evidence, then resumes under
    `connect.<output>` — via a `ConnectorProvider` port +
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
  `/v1/agent-runs/{run_id}`; module `agent-manager`. A real OpenAI-compatible HTTP AI provider ships
  (env-configured); the deterministic Stub is explicitly opt-in for development and tests, and a
  missing provider makes AI operations fail loudly.
  Enabling refactor: hoisted `eventlog.AppendJSON` (the marshal→append spine). A flow's **AI node** runs
  an agent during a decision only when the interpreter reaches it; the shell
  passes its literal prompt or exact current record, records effect evidence,
  and resumes under `ai.<output>` — via an
  `AgentProvider` port + `agents.Provider` adapter wired at the composition root, so the engine never
  imports the Agent Manager (same one-way wiring as features/connectors). **Human-in-the-loop**:
  escalating a run opens a Case Manager case — the Agent Manager (built later) emits the Case Manager's
  own `ReviewRequested` event the `cases` projector already consumes, with the run in the case context
  (one-way direction, no `cases` change). **Monitoring**: `GET /v1/agent-runs/summary` rolls up the run
  log (totals, completed/failed, by agent). The **agents UI** (`web/src/routes/agents`) lists/defines
  agents with a run-summary banner, and a per-agent view runs the agent, shows the run log, and
  escalates a run. Full test pyramid (unit/integration/API-e2e/Playwright); all CI gates green.
  The original Phase 4 deferrals have since shipped: tool calling, structured-output validation,
  asynchronous execution, and a real OpenAI-compatible provider are all part of the delivered track
  recorded in `BUGS.md`.
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

**MVP roadmap complete (Phases 0–5), plus a large post-MVP hardening + enterprise track.** The
per-slice narrative that used to live here has been archived — the authoritative slice-by-slice history
(every PR, every audit round, every deferred item) is in **[BUGS.md](BUGS.md)**. What that track
delivered, by theme:

- **Durability & scale-out backbone:** durable SQLite + **Postgres** projection stores; a streaming
  (offset-indexed) file WAL; a shared SQLite event log and a **NATS JetStream** networked log for the
  split-services profile; Postgres `LISTEN/NOTIFY` fast path.
- **Governance & change control:** RBAC (viewer→operator→editor→approver→admin); **four-eyes
  maker-checker** on flow deploys, model versions, and operational policy versions, with pre-approval
  binding + environment-scope gating; immutable AI-agent version pins in governed flows; flow
  **assertions + promotion gates**; candidate-faithful **shadow deploys** with independently resolved
  dependencies, policy-aware matching, and homogeneous evidence cohorts; instant rollback; comment
  threads + @-mention notifications inbox on every reviewable subject.
- **Enterprise identity:** OIDC SSO (Google, Cognito, generic) + **SAML 2.0**; **SCIM** user/group
  provisioning + deprovisioning honored by live sessions.
- **Decisioning depth:** decision-table hit policies + aggregators; **ML model hosting**
  (logistic/GBM/expression/external) with a **Predict node**; an external-decision compatibility API;
  governed stable-cohort experiments + monitors (**PSI drift**, covariate drift,
  decision-linked corrected business outcomes with engine-derived treatment/model
  lineage and homogeneous model-version cohorts) + SLOs.
- **Experimentation & population automation:** hypothesis-driven experiment aggregates with
  exact-version arms, reached exposure, maker-checker production launches, confidence/effect/SRM/
  guardrail analysis, and safe promotion; durable decision/backtest population jobs with immutable
  manifests, per-item idempotency, cross-replica worker recovery, full lifecycle control, partial
  failure, downloadable results, and retention.
- **Model-risk & governance packaging:** **SR 11-7 / SS1/23 model inventory** (`mrm/`) across flows,
  models, and agents; AI/ML governance — agent registry/versioning, offline eval, guardrails, cost
  attribution; structured **reason codes** end-to-end.
- **Compliance & data protection:** AES-256-GCM at rest with AAD binding, **crypto-shred erasure**,
  **PII masking** at the read boundary, OTel tracing, an SSRF egress guard, and a full audit surface.
- **Persona-adaptive UI:** per-persona surfaces (Builder, Operator, Risk, Team Manager,
  Experimentation, Executive, Evaluator) over an API-first design — OpenAPI 3.1 + Go/TypeScript SDKs.
- **Hardening:** eleven+ multi-agent audit rounds (correctness, security, fake-hunting,
  accessibility/WCAG-AA-in-CI, live-UI walkthroughs) — see the R-/DR-/BF- blocks in `BUGS.md`.
- **Shared-VPC deployment:** the Amazon Elastic Container Service Terraform module preserved its
  standalone dedicated Amazon API Gateway VPC Link while also accepting an existing link and its
  security group as an inseparable pair under an explicit plan-known ownership switch. Shared
  environments created neither duplicate resource even when both coordinates came from resources
  whose IDs were unknown during planning, and the task ingress and API integration used the same
  supplied coordinates. Explicit state moves preserved standalone installations across the
  resource-address change. The reusable module declared, rather than embedded, its default and
  `us-east-1` provider requirements so environment roots supplied both configurations explicitly;
  plan-contract tests ran without AWS credentials in pre-commit and CI.

---

## 8b. Forward roadmap — enterprise decision automation, modeling, and tracking

The product is well beyond a prototype: all four product components are real, the hosted demo uses the
real Go backend, and the governed/event-sourced foundation is unusually deep. It is not yet a complete
enterprise decision platform. The remaining work is concentrated in execution integrity, operational
depth, model/data-science workflow, and production evidence rather than endpoint count. A whole-product
capability audit and the competitor comparison (**[docs/COMPETITIVE.md](docs/COMPETITIVE.md)**) establish
the current boundary. Competitor entries are vendor claims, not independently tested facts.

Some of the earlier gap list has already been worked through: fair-lending screening, the
adverse-action arc, independent model validation, governed policy/agent dependencies, durable human
tasks, candidate-faithful shadows, and decision-attributable model actuals shipped in Phases 6, 7, and
11 and the subsequent journey audits.
On scale: there is now a **decision-throughput benchmark** (`make bench`, `decision-engine/decide_bench_test.go`)
with a recorded baseline (see `docs/PERFORMANCE.md`) — the synchronous decide path is race-free under the
detector and scales across cores. Decide-boundary failure injection now covers a failing dependency
(Connect/AI/Predict) AND a dying store mid-decide, and a soak test drives sustained concurrent load over
the segmented, self-archiving WAL asserting no drift; still open are the projection runtime's single-node
ceiling and a multi-hour endurance run. Durable WAL, SQLite, and real-Postgres decide baselines are
recorded in `docs/PERFORMANCE.md`.
A **production-readiness audit** (2026-07-27, BUGS.md) then closed the load-balancer/multi-replica gaps
that only appear in the deployment shape this project recommends: the Postgres log lost events under
concurrent appends (seq assigned at INSERT, visible at COMMIT — appends now serialize on an advisory
lock so commit order is seq order), the launch origin served the app shell to anonymous browsers instead
of failing closed, `--log=file` in production warned rather than refused, and SIGTERM closed the listener
without first failing readiness, so every rolling deploy dropped requests. Note the cost that buys the
first of those: **appends to the Postgres log are now globally serialized**, so write throughput under a
networked log is bounded by that lock — measuring it is part of the still-open durable-log throughput
work above.
A **whole-product journey audit** (2026-07-27, BUGS.md) then followed every core journey across its
UI control, HTTP command, event/projection fold, background scheduler, notification handoff, and
restart/replay behavior. It closed statutory-retention bypasses, scheduled-deployment races and
maker-checker gaps, non-durable SLA/agent escalations, disconnected model/approval/actuals and
erasure surfaces, stale shared approval tasks, and suspended decisions whose case could terminate
independently. The scheduler now participates in health, operational reads fail visibly instead of
becoming reassuring empty states, Test names and enforces the published/deployed version it runs,
and every documented discussion subject deep-links from an inbox notification to the exact thread.
`docs/JOURNEYS.md` remains the executable product contract for these cross-component seams.
Nothing here is a claim that a competitor is beaten — only a list of gaps to work through. The roadmap
orders them hardest-blocker-first; each phase is a direction, not a committed date.

### 8b.1 Delivery shape — one serialized large PR per complete outcome

The current program is deliberately carved into **large-to-huge vertical pull requests**. LLM-assisted
implementation and review make broad coherent changes tractable; splitting one user journey across
many small PRs would instead create temporary contracts, duplicated migrations, and misleading partial
UI. Size is not a reason to omit a required layer.

The PRs below are **strictly serialized**. Only one may be open at a time. After it merges, fetch and
reconcile the new `origin/main`, regenerate any derived artifacts, and only then begin the next PR.
Every PR must:

- deliver its named journeys through domain logic, command/event contracts, projections, HTTP,
  OpenAPI/SDKs, schedulers or workers, UI/UX, permissions, audit/notifications, replay/restart, and
  deployment configuration wherever those layers participate;
- preserve deterministic replay and the functional-core/imperative-shell boundary; recorded effects
  are explicit data, never hidden I/O during replay;
- include unit, integration, assembled HTTP, multi-replica/failure-injection, native browser,
  real-Wasm, and embedded-artifact coverage proportional to the slice;
- migrate old event/projection shapes deterministically or fail with an explicit compatibility error;
- update `PLAN.md`, `BUGS.md`, `docs/JOURNEYS.md`, API examples, and performance/security evidence in
  the same PR so documentation never describes an aspirational contract as shipped; and
- announce and approve any new dependency before it is introduced, including its owner, license,
  security posture, and the in-repository alternative.

### 8b.2 PR E1 — durable execution integrity

**Outcome:** a caller can submit or resume a decision or agent run through failures, retries, restarts,
and replica changes without executing unreachable graph effects or silently creating a second logical
run. This PR is the prerequisite for every later tranche.

**Implemented in the E1 branch:** the pure interpreter now yields only reached
Connect/AI/Predict effects with exact upstream state; requested, succeeded, and
failed evidence plus delivery guarantees are durable. Decision admission has
idempotency, caller tracking, metadata/control validation, history indexes, and
lease-owned recovery; async agents have durable admission, claims, heartbeats,
cancellation, timeout, explicit retry, and dead letter. Manual-review, shadow,
preapproval, and consent side effects are crash-idempotent. The HTTP/OpenAPI,
Go/TypeScript SDK, operator UI, observability, counterfactual, and record-free
Coverage contracts share those semantics. Production deployment is explicitly
split into scalable API and worker tiers plus a singleton scheduler in both
Helm and the AWS ECS module.

The compatibility boundary is explicit: pre-E1 `DecisionStarted` events are
identified by the event shape introduced alongside recovery leases and replay
their already-recorded prepared namespaces without re-reading mutable features,
consent, or providers. New executions record separate context and effect
evidence. The complete E1 exit matrix passes locally: `make check`, `make ci`,
18 Terraform plan contracts, Helm lint/render, 228 frontend unit tests, 125
native browser journeys, 83 real-Wasm journeys over the regenerated 7,999-event
history, and 3 embedded-production smokes.

**Scope:**

- Replace eager Connect/AI/Predict preparation with a pure, resumable graph interpreter. The core
  advances until it reaches an effectful node and yields a typed effect request containing the exact
  current node input; the shell authorizes, executes, records, and feeds the result back before the core
  advances. Untaken branches request no effects. Assignment/Rule/Code outputs are therefore visible to
  downstream effects.
- Give every effect a deterministic identity and durable requested/succeeded/failed evidence. Make
  replay consume the recorded result and recovery continue from the last committed interpreter state.
  Pass provider idempotency keys where supported; for providers that cannot guarantee effectively-once
  behavior, expose the at-least-once risk explicitly rather than claiming impossible exactly-once
  semantics.
- Add a tenant/workspace/flow/environment-scoped decision idempotency contract, a caller business
  reference and correlation id, bounded persisted metadata, and validated per-call controls. An
  identical retry returns the original logical result; reuse with a conflicting payload fails loudly.
  Index and search decision history by business reference, entity, status, and supported metadata.
- Define interrupted decision states and recovery ownership. A durable recovery worker resumes safe
  work, times out or abandons work that cannot continue, and surfaces attempts and the last error in
  history, observability, and the decision UI.
- Give async agent runs distributed claims with owner, lease, heartbeat, attempt, cancellation,
  timeout, and retry/dead-letter state. Recovery belongs to a worker tier and cannot be performed
  independently by every API replica. Tool/provider calls obey the same durable effect rules.
- Reconcile preview, shadow, batch, manual-review suspend/resume, model actuals, consent/sharing gates,
  audit, metrics, notifications, and the Wasm transport with the new interpreter and invocation
  contracts.

**Exit evidence:**

- tests prove an effect on an untaken branch is never authorized or called, and a reached effect sees
  all upstream mutations;
- repeated and concurrent submissions with one idempotency key produce one decision and one set of
  effects, while a conflicting reuse is rejected;
- failure injection at every event boundary resumes to the same trace/output without losing the
  decision or duplicating an effect the provider can deduplicate;
- several API and worker replicas recover one agent run with one active lease and one terminal
  outcome; lease loss, cancellation, timeout, and dead-letter paths are replay-stable; and
- the browser can find a run by business reference/entity, explain its attempts/effects, and distinguish
  running, suspended, retrying, abandoned, failed, and completed states.

### 8b.3 PR E2 — experimentation, outcomes, and population automation

**Outcome:** champion/challenger becomes an auditable experimentation product rather than per-request
traffic splitting, and operators can run large populations as durable jobs.

**Implemented in the E2 branch:** `decision-engine/experiments` owns exact-version
multi-arm specifications, deterministic salted subject assignment, reached exposure,
lifecycle scheduling, production launch review, statistically guarded analysis, and
governed promotion. `decision-engine/outcomes` records idempotent binary/continuous
business facts with source, observation, label-version, and immutable correction
history while deriving flow, treatment, and model lineage from the completed
decision. Fair-lending reports, model performance, and shadow evidence accept or
retain the same exact experiment/cohort/arm dimensions.

`decision-engine/population` provides decision and record-free backtest jobs with
immutable bounded manifests, per-item identities and attempts, concurrency limits,
distributed claims/heartbeats, pause/cancel/resume/retry, partial failure, NDJSON
results, retention, and scheduler/worker ownership. Expired claims recover even when
the job is at its concurrency limit. The OpenAPI document, Go/TypeScript SDKs,
Experimentation-persona navigation, list/detail operator surfaces, notifications,
seeded real-Wasm history, and maker-checker handoffs expose the same contracts.
The local E2 release matrix passes `make check`, `make ci`, 232 frontend unit
tests plus the production build, 128 native browser journeys, 83 real-Wasm
journeys, 3 embedded-production smokes, all 18 Terraform plan contracts, and
the container publication/retention contract.

**Scope:**

- Introduce a first-class experiment aggregate with hypothesis, owner, subject-key expression,
  champion/challengers, allocation, salt, environments, eligibility, primary KPI, guardrails,
  minimum sample/effect, start/stop windows, and draft/running/paused/completed/cancelled states.
- Assign cohorts deterministically from the stable subject key and experiment salt. Record an exposure
  only when execution reaches the experimental treatment; keep allocation stable across retries,
  replicas, and restarts, and start a new cohort when material configuration changes.
- Generalize outcome ingestion from model actuals into idempotent, decision-linked business outcomes
  with event time, observation window, source lineage, label version, and correction history. Join
  exposures to outcomes without caller-authored model or treatment facts.
- Calculate sample sizes, conversion/continuous metrics, confidence intervals, effect size, sample-ratio
  mismatch, guardrail regressions, and inconclusive results. State statistical assumptions and avoid
  presenting directional movement as proof.
- Unify A/B, shadow, backtest, model actuals, fair-lending slices, promotion evidence, and experiment
  dashboards around consistent cohort dimensions and exact deployed versions.
- Add a durable population-job resource for bulk decisions/backtests: immutable input manifest,
  progress, per-item idempotency, bounded concurrency, pause/cancel/resume/retry, partial failure,
  downloadable result manifest, retention, and scheduler/worker ownership. Preserve NDJSON as a
  streaming convenience, not the durable job contract.
- Provide Experimentation-persona setup, launch review, live health, outcome analysis, decision
  drill-down, and promote/stop journeys with maker-checker controls where production behavior changes.

**Exit evidence:** repeated subjects never cross arms within a cohort; exposure/outcome attribution is
version-coherent after replay; statistical fixtures reproduce known results and edge cases; a killed
multi-replica worker pool resumes a large population job without lost or duplicated logical items; and
the UI never labels an underpowered or invalid experiment a winner.

### 8b.4 PR E3 — enterprise case operations

**Outcome:** Case Manager becomes a configurable operational workbench for high-volume review teams,
not only a durable generic review queue.

**Scope:**

- Add versioned case-type schemas, required fields, state machines, dispositions, reason codes,
  priorities, service calendars, evidence requirements, and role-aware form/view layouts.
- Add configurable queues and routing rules over case/entity/decision attributes, reviewer skills,
  capacity, jurisdiction, priority, ageing, and conflict-of-interest constraints. Make claims,
  reassignment, escalation, and routing atomic across replicas.
- Support linked cases, alerts, decisions, entities, agent runs, connector evidence, notes, and
  immutable attachment metadata. Introduce a pluggable binary-artifact boundary only after the storage
  dependency decision is approved; audit downloads and retention/hold behavior.
- Add saved views, full-text/filter search, bulk assignment/status/disposition actions, duplicate
  detection, queue rebalance, and keyboard-efficient review. Human-task completion must continue to
  resume the exact suspended decision version.
- Add QA sampling, second review, override tracking, reviewer feedback, workload/capacity,
  time-to-first-action, resolution time, backlog ageing, SLA breach, routing failure, and bottleneck
  analytics. Feed validated reviewer outcomes into agent/model evaluation without treating a single
  reviewer as ground truth.
- Integrate notifications, webhook delivery attempts, retry/dead-letter operations, audit exports, PII
  masking, lawful basis, retention, legal hold, and erasure into the case/evidence model.

**Exit evidence:** browser journeys cover case-type administration, automatic routing, an end-to-end
review/resume, attachments/evidence, bulk work, QA disagreement, SLA escalation, and operational
analytics; concurrent reviewers cannot both claim or terminally decide one task; and rebuild/restart
reproduces queue membership, timers, evidence, and metrics.

### 8b.5 PR E4 — collaborative authoring and governed delivery

**Outcome:** teams can safely co-author and reuse decision assets without relying on one browser's
in-memory canvas.

**Scope:**

- Add server-persisted flow drafts with autosave, explicit draft ownership, optimistic concurrency,
  conflict presentation, recoverable local changes, and presence. Never silently overwrite another
  editor's work.
- Add versioned, immutable reusable subflows/components with typed input/output contracts, dependency
  pins, impact analysis, cycle detection, upgrade previews, and exact runtime lineage.
- Make comments, mentions, proposed changes, review assignments, diffs, assertions, backtests,
  experiment evidence, and approvals part of one change-set journey. Show which deployed environments
  and downstream assets a change affects.
- Add repository-friendly export/import and validation commands for flows, policies, models,
  experiments, case definitions, and environment bindings. Provide deterministic canonical formats,
  semantic diff, CI validation, and guarded promotion without creating an alternate governance bypass.
- Extend search and the command palette across drafts, reusable assets, experiments, case types, and
  dependency relationships; preserve persona-specific density and WCAG-AA behavior.

**Exit evidence:** two browsers can edit one draft with visible conflicts and no data loss; a reusable
subflow upgrade shows and tests every dependent flow before approval; imported/exported assets
round-trip deterministically; and no Git/API/UI route can deploy unreviewed production logic.

### 8b.6 PR E5 — model and context data-science platform

**Outcome:** modelers can build reproducible datasets and governed model releases, while decision flows
consume typed, observable, point-in-time-correct data.

**Scope:**

- Add versioned entity/event schemas, relationships, source ownership, freshness and completeness
  contracts, data classifications, lineage, and quality incidents. Validate ingestion at the boundary
  while retaining an explicit governed evolution path.
- Add streaming ingestion and incremental/materialized features with late-event and correction
  semantics, backfill jobs, point-in-time joins, freshness SLOs, lineage, and cost/cardinality
  observability. Retain the deterministic on-demand feature path as a correctness reference.
- Add a dataset registry with immutable snapshots/manifests, feature/label definitions, time cutoffs,
  consent/purpose scope, train/validation/test partitions, provenance, retention, and reproducible
  export.
- Add training/evaluation jobs and a signed model-artifact registry. Support richer runtimes through a
  versioned serving protocol or approved sandboxed artifact runtime; do not embed arbitrary Python or
  native code in the decision process without an explicit isolation and dependency decision.
- Add calibration, threshold optimization, segment analysis, faithful model-specific explanations,
  multi-class/regression outcomes, automated label pipelines, performance confidence intervals, and
  challenger comparison.
- Extend fair-lending analysis beyond the current AIR screen with minimum-sample rules, confidence
  intervals/significance, intersectional groups, regression or matched-pair methods, missing-data
  treatment, documented limitations, and examiner-reproducible snapshots. Keep every report a screen,
  not an automated legal conclusion.
- Unify data, feature, dataset, model, validation, approval, deployment, prediction, explanation,
  actual, drift, fairness, and retirement lineage in the Model Risk and UI journeys.

**Exit evidence:** a model release can be reproduced from an immutable dataset snapshot and artifact;
offline and online feature values agree on correction/late-event fixtures; stale or poor-quality data
blocks or visibly degrades according to an explicit policy; explanation/calibration/fairness fixtures
are independently reproducible; and deployment still requires current independent validation and
four-eyes approval.

### 8b.7 PR E6 — production scale, tenancy, and disaster recovery

**Outcome:** the recommended production topology has measured capacity, durable work ownership,
operator-controlled tenancy, and routinely tested recovery rather than a runbook-only promise.

**Scope:**

- Remove the global serialized-append ceiling without reintroducing commit-order gaps; partition work
  and projections by explicit tenant/stream ownership, preserve a canonical ordering contract where
  required, and publish the consistency model.
- Horizontally scale projection and worker ownership with durable leases/rebalancing. Run schedulers as
  redundant replicas with leader/work claims rather than a deployment-enforced singleton.
- Establish published workload profiles and SLOs for interactive decisions, effectful decisions,
  event ingestion, projections, agent jobs, cases, and population jobs. Add multi-hour endurance,
  overload/backpressure, noisy-neighbor, rolling-deploy, dependency-outage, and regional-latency tests.
- Automate encrypted backups, point-in-time recovery where supported, restore validation, integrity
  reconciliation, and scheduled disaster-recovery exercises. Measure and publish achieved RPO/RTO;
  surface last successful backup/restore drill and recovery health to operators.
- Add organization/workspace lifecycle APIs and UI: creation, invitations, membership, local/SSO/SCIM
  role mapping, workspace isolation, API-key/service-account ownership, quotas, rate limits, spend
  limits, suspension, export, and deletion. Make tenant placement and data residency explicit.
- Define and test workload-isolation profiles for self-hosted single tenant, shared control plane, and
  stronger per-tenant compute/storage isolation. Prevent cross-tenant cache, log, metric, trace,
  artifact, secret, and support-tool access.
- Formalize API compatibility/deprecation policy, generated and packaged SDK releases, audit-log
  export, operator upgrade/rollback procedures, schema/event compatibility checks, and zero-downtime
  migrations.

**Exit evidence:** the documented production profile survives replica loss and rolling upgrades with no
lost committed work; sustained tests meet published SLOs with bounded queues; restore drills rebuild and
reconcile a production-shaped tenant inside the published RPO/RTO; scheduler/worker failover is proven
with multiple live replicas; and automated isolation tests attempt cross-tenant access at every storage
and observability boundary.

### 8b.8 PR E7 — ecosystem and regulated solution packs

**Outcome:** the platform is adoptable for complete credit, fraud, AML/KYB, and servicing workflows
without pretending that a handful of generic adapters constitutes a data marketplace.

**Scope:**

- Publish a connector/provider SDK and conformance harness covering schemas, auth/secrets, egress,
  idempotency, pagination, webhooks, rate limits, retries, circuit breaking, sandbox fixtures,
  observability, data classification, consent/purpose, and replay evidence.
- Deliver coherent provider packs prioritized by validated customer journeys, with normalized entities,
  reason/evidence mapping, documented commercial prerequisites, and maintained contract tests. Provider
  count is not a success metric; supported end-to-end journeys are.
- Ship governed solution templates for credit origination/line management, fraud review, AML/KYB,
  sanctions/adverse media, bank-statement analysis, and servicing. Include flow, policy, model/agent,
  case type, queue, experiment, dashboard, compliance, and sample-data assets as one installable pack.
- Add actual delivery providers and attempt tracking for adverse-action notices and other required
  communications; retain the exact issued artifact independently of delivery. Add subject access and
  portability exports over the governed data inventory.
- Add AML investigation and regulatory-report preparation workflows, including evidence lineage,
  approvals, amendments, submission/download boundaries, retention, and explicit human accountability.
  Never represent generated narratives as filed reports unless a real filing integration confirms it.
- Add domain-agent evaluation suites, reviewer-feedback loops, safety policies, cost/latency budgets,
  escalation defaults, and aggregate quality reporting before marketing an agent as production-ready.

**Exit evidence:** each published solution pack runs as a seeded native/Wasm journey over real backend
contracts; connector conformance failures block release; communications prove generation, delivery
attempts, and exact artifacts separately; and every claimed regulatory workflow identifies which steps
are automated, human-approved, externally submitted, or intentionally outside the product.

### 8b.9 Parallel organisational release track

No code PR can manufacture customer trust or regulatory evidence. In parallel with E1–E7, a production
offering requires SOC 2 Type II and ISO 27001 programs, independent penetration tests, threat-model and
incident-response exercises, on-call and support processes, privacy/legal review, model-validation
staffing, data-provider agreements, regional subprocessors, recovery exercises, and reference
deployments. Marketing and enterprise-readiness claims must distinguish implemented controls from
audited certifications and operating history.

### 8b.10 Earlier regulated-lending and production phases

The phases below record the already delivered or partially delivered track that preceded E1–E7. Open
tails have been absorbed into the PRs above; they remain here as architectural history, not a second
competing queue.

- **Phase 6 — Fair lending & adverse action — ✅ DONE.** The `fairlending/` package: (1) a read-only
  **disparate-impact report** — the adverse-impact ratio (four-fifths rule, ECOA/Reg B) of
  favorable-outcome rates across a protected-class attribute, folded from the recorded decision history,
  with CSV/Markdown export. It is a screen, not a legal conclusion, and states what it excludes
  (referred, no disposition, attribute absent). (2) A **per-flow config** (event-sourced) declaring the
  protected attribute, favorable outcome, and AIR threshold — a first-class flow artifact the report and
  the governance surface both read (the report runs from it when the query omits params). (3)
  **Adverse-action notice generation** — the ECOA/Reg B notice for a declined decision, rendered from
  its recorded reason codes (up to four principal reasons) plus a per-workspace creditor-identification
  setting; it errors rather than emit an incomplete notice. (4) A **regression fires on the governance
  surface**: a configured flow whose AIR falls below its threshold shows as an MRM open issue, like any
  other check. Admin-gated report/config/settings; operator-gated notice; `/fairlending` page (config
  save + settings) and an adverse-action download on the decision page. Zest AI's tooling was the scope
  reference; independent model validation of the fair-lending model itself shipped in Phase 7.
- **Phase 7 — Model governance parity — ✅ DONE.** Models now carry a **version** (each redefine bumps
  it) and a **four-eyes approval** (`ModelApprovalRequested/Approved/Rejected`): a maker requests, and a
  checker who is neither the requester nor the version's author approves — a redefine invalidates a
  prior approval, the same "changed logic, re-review" rule flows follow. Enforcement mirrors flows:
  **outside the sandbox, a Predict node refuses a model whose current version is not approved**.
  **Independent validation evidence** (`ModelValidationRecorded`: dataset, named finite metrics,
  authenticated validator, substantive notes, pass/fail) attaches to a version. The owner cannot
  validate their own model, the endpoint requires the approver role, and approval is refused until the
  latest independent current-version record passes. The **MRM inventory** classifies that evidence as
  tested/failing/none separately from the drift baseline and flags an unapproved or unvalidated model
  as a governance gap. The models page carries the complete notification → validation → decision
  handoff. The demo seed runs every model through different-actor validation + approval.
  The whole-product governance audit also closed the adjacent mutable-dependency seam: policy
  publication is now sandbox iteration, staging/production retain the last four-eyes-approved policy
  version, and every decision snapshots its selected policy before execution so a durable resume
  cannot change logic mid-flight. AI nodes carry an immutable agent version; a positive version is
  required for non-sandbox decisions and previews, making an agent change an ordinary reviewed
  flow-version deployment rather than an invisible mutation behind the flow.
- **Phase 8 — Production hardening at scale — 🚧 partial.** The suspected multi-replica double-apply
  was **confirmed with a test** (two runtimes sharing one durable store applied each event to a
  non-idempotent counter twice — count 2N) and **fixed**: the incremental apply now reads the **durable
  checkpoint under a lock inside the apply tx** (Postgres `SELECT … FOR UPDATE`; SQLite's `Begin` holds
  a global writer lock) and skips an event another replica already applied — so N replicas fold each
  event exactly once between them. Proven on both SQLite (writer-mutex path) and **real Postgres**
  (`FOR UPDATE` path), race-clean. The **bootstrap cold-start** was likewise confirmed with a test (two
  replicas rebuilding a fresh pre-populated store concurrently drifted off the true count) and closed:
  the durable bootstrap now runs in **one lock-coordinated transaction** (create the checkpoint row via
  insert-if-absent, lock it, then reset+replay+checkpoint atomically), so concurrent boots serialize —
  one builds, the rest see the checkpoint already at head and do nothing. A projection **benchmark**
  (`BenchmarkDurableApply`) and a **Postgres CI job** (runs the DSN-gated store/log/projection tests
  against a live Postgres — no longer skipped everywhere) landed too. **Log compaction/archival now ships:**
  the file WAL is segmented — it seals `events.log` at a size cap and gzip-archives older sealed segments
  in place, bounding on-disk size while every event stays readable and seq stays positionally stable; a
  soak + store-failure-injection suite backs it. _Still open (the ops-heavy tail):_ backup automation and
  a multi-hour endurance run.
- **Phase 9 — Connector resilience & data sources — 🚧 partial.** **Resilience done:** every outbound
  connector call now runs through the retry budget + a per-connector **circuit breaker**
  (`connectors/resilience.go`), applied once at the `InvokeWithSecrets` choke point so every connector
  (HTTP, GraphQL, Plaid, Stripe, credit-bureau) gets it. A **transient** error (timeout, connection
  failure, upstream 5xx/429) is retried with capped exponential backoff; a **permanent** one (4xx, bad
  config/body) fails immediately and does not trip the breaker. After repeated transient failures the
  breaker **opens and fails fast** for a cooldown (then half-opens for a probe) — so a down bureau does
  not make every decision hang through the full timeout×retry budget. The per-call timeout already
  existed. Replay-safe: a connector fetch is a runtime effect whose response is recorded once, so
  retries/breaker never touch a replay. _Still open (data-provider work, not pure code):_ the breadth of
  real provider adapters — intraktible has ~9 connector types (incl. credit-bureau + sanctions
  normalizers) vs the ~270 / ~200 sources Alloy and Taktile advertise, which is commercial-relationship +
  per-API-spec work.
- **Phase 10 — Command-path performance — ✅ DONE.** Two O(n) reads fixed. (1) The flow/model
  maker-checker folds (`foldTenant`, `foldRequest`, `foldModelGov`, `deployHistory`) read the **entire,
  decision-dominated log** on every deploy/publish/approve; the `Log` interface now carries a **required**
  `ReadTenantStream` — indexed `(org, workspace, stream, seq)` on the durable logs (a new index),
  filtered scan on the index-less ones — so those folds scan the flow/model events, not the whole log.
  (2) `history.ListPage` loaded **every full decision record** (input + node trace + output) to filter
  and paginate; it now filters/sorts/counts over a **lightweight index** (a per-decision summary the
  single `history.Projector` maintains alongside the record) and loads full records **only for the
  window it returns** — generalizing the audit-index pattern. An index entry with no record fails loud
  (projection inconsistency), never a silent skip.
- **Deployment — shared Amazon Elastic Container Service environments.** The Amazon ECS Terraform
  module can reuse an existing VPC, private subnets, and cluster; it configures a generic OpenID
  Connect provider without exposing the client secret in Terraform state. Its `api_always_on` mode
  keeps one API task running, sets the autoscaling floor to one, and omits the idle reaper so a shared
  development environment remains available continuously. Always-on deployments can serve the UI
  embedded in the production binary through the existing Amazon API Gateway and CloudFront path,
  rather than depending on a separate static-site upload. Amazon ECS service creation waits for
  every injected AWS Secrets Manager secret version, including the database DSN that becomes
  available only after Amazon RDS has finished provisioning. The generic OpenID Connect
  deployment coordinates include an explicit organization and workspace, preserving the
  application’s tenant-bound identity invariant. Every merged `main` commit publishes one
  immutable 12-character commit-SHA release group: a multi-architecture manifest plus directly
  selectable `-amd64` and `-arm64` images. Mutable branch tags and semantic-version tags were
  removed, and GitHub Container Registry retention keeps the newest 20 complete release groups while
  deleting untagged, malformed, and obsolete versions and enforcing a 60-version package ceiling.
- **Phase 11 — Regulatory data lifecycle — 🚧 partial.** **Legal hold + automated retention shipped**
  (`platform/erasure`). Legal hold: a subject can be put under a legal/litigation hold, which makes it
  **survive retention** and **blocks erasure** (destroying data under hold is spoliation) — `Erase`
  refuses a held subject with `ErrHeld` (a 409, "release the hold first"), serialized with the
  crypto-shred so a hold can't race a shred. Automated retention: a **per-tenant retention policy**
  (opt-in, off by default) drives a **scheduled sweep** (`erasure.Scheduler`, on the shared sweep
  cadence) that crypto-shreds subjects past their window and **skips held subjects** — a tenant with no
  policy is never swept, so the timer never erases data no one asked to expire. Admin endpoints:
  hold/release/list-held, get/set retention-policy. **Consent/purpose ledger shipped**
  (`platform/consent`): a data subject's consent to process their data for a named purpose, recorded as
  events (grant/withdraw) so the history is auditable, with a GDPR Art. 6 lawful basis and optional
  expiry. `Has(subject, purpose, now)` answers "may we use this data for this purpose right now?" (honors
  withdrawal + expiry); `List` returns a subject's consents. **Consent is now wired into the decision
  journey** (the business, not the end customer, provides it): a **Connect node can declare
  `requires_consent`** and the decide path **refuses to pull that data source** without the subject's
  active consent (FCRA permissible purpose) — failing loud, never fetching; a caller may also **assert
  consent in the request** (the bank passing through what it obtained), captured under the subject before
  the gate runs. The subject is the decision's entity (`ref.Key()` = `type/id`), the same key PII
  sealing and erasure use — so a data subject is identifiable across consent, PII, holds, and erasure
  (the substrate for GDPR responses). A **compliance operator manages consent on the subject's entity
  page** (grant/withdraw/review), and the demo seed records consent for its applicant/customer entities.
  **Records now carry evidence and are reframed as a lawful-basis record** (research: US/UK/EU +
  ISO 27560/Kantara, see `docs/CONSENT.md`). Cross-jurisdiction research found consent is usually the
  _wrong_ basis for credit decisioning (power imbalance → not freely given; the ICO's own worked example
  is a credit-reference pull) — so a grant records the Art. 6 basis (contract/legitimate*interest for
  decisioning, not consent) plus optional **`Evidence`**: how it was obtained (a controlled vocabulary),
  a reference to the signed artifact in the controller's own store, a **content hash** for tamper-
  evidence, and the **notice version** shown. The subject's data page lets an operator attach a file that
  is **hashed in the browser (SHA-256)** — only the fingerprint + name are stored, the document's bytes
  never leave the tenant (data residency). The demo seed uses the correct basis and a worked evidence
  record for applicants. **Adverse-action issuance is now a durable record**, the mirror of consent
  (consent gates the data *pull*; adverse action governs a decline's *output*). The stateless ECOA
  notice render became an auditable issuance — `POST /v1/decisions/{id}/adverse-action/issue` records
  who served the notice, when, by what delivery method, citing which principal reasons, plus a SHA-256
  hash and the exact immutable rendered document (the proof ECOA/Reg B expects within 30 days). A
  dedicated issued-artifact download verifies that hash and never re-renders mutable settings. The notice gained the
  **FCRA §615(a)** disclosures (consumer-reporting-agency identity, "the CRA did not make the decision",
  right to a free report + to dispute) for report-based declines, failing loud if the CRA is
  unconfigured. A **pending-notices work queue** (`GET /v1/adverse-actions`) surfaces declines awaiting
  a notice with their age (the 30-day clock); a compliance operator issues from the decision page, and
  the demo seeds both issued and pending notices. **Automated-decision human review is now recorded**
  (GDPR Art. 22 human intervention / ECOA reconsideration): a decision engine can decline someone with
  no person in the loop, so the `reconsideration` package records that a human upheld or overturned that
  solely-automated decline — basis, outcome, and a *required rationale\* (a review with no reasoning is
  the rubber stamp Art. 22 forbids), keyed to the decision. Eligibility fails loud unless the decision is
  a completed, solely-automated decline (`history.Record.HumanReviewed`, set on resume, excludes
  decisions that already had a person in the loop); the original decision stays immutable. The decision
  page shows a human-review panel for solely-automated declines. A **compliance operator dashboard**
  (`/compliance`) now unifies the whole arc for a compliance officer: the adverse-action 30-day queue
  (overdue flagged), the human-review audit trail, the lawful-basis / consent overview (by basis, with
  withdrawn + expiring counts), and an admin-only data-governance card (retention, legal holds, erased
  subjects). Cards degrade per role — the queue/audit/consent reads are viewer-level, so a compliance
  _viewer_ can work it; the governance card is admin-only. A tenant-global consent list
  (`GET /v1/consent/records`) was added, and the adverse-action queue GET relaxed to viewer (a read).
  The dashboard exports **examiner-ready compliance registers** (`registers` package): the
  adverse-action register (ECOA/Reg B record of adverse actions taken), the human-review register
  (Art. 22 reconsiderations), and the lawful-basis register (consent), as CSV (formula-injection-safe)
  or Markdown — the artifact a lender produces on examination.
  **GLBA sharing opt-out** (`platform/sharing`) is the opt-out mirror of consent: consent is opt-in and
  gates an inbound data pull; this records a consumer's election to stop their NPI being shared with
  nonaffiliated third parties (GLBA §6802), and the decide path blocks a Connect node marked
  `shares_npi` once the subject has opted out (fail-loud, the share never happens). Managed on the entity
  page, surfaced on the compliance dashboard.
  **Retention-clock enforcement** (`retention`) closes the data-lifecycle loop: it computes how long a
  subject's records must be kept — ECOA / Reg B §1002.12 (25 months) over their adverse-action
  issuances + credit decisions — and the erasure endpoint now **refuses** a subject still inside that
  window (a `RetentionGate` on the erasure service; GDPR Art. 17(3)(b) exempts erasure where retention
  is a legal obligation) — the automatic counterpart to a manual legal hold. The entity page shows the
  subject's retain-until. A **GDPR Art. 22 decision-explanation** artifact
  (`GET /v1/decisions/{id}/explanation`) now assembles a decision's recorded logic into a
  subject-facing "how this was decided & your rights" document — solely-automated flag, outcome,
  principal factors, and the Art. 22(3) rights (human intervention / contest / explanation), folding
  in any recorded human review, and naming the reconsideration channel. It is distinct from the ECOA
  adverse-action notice (a US decline letter) — this is the GDPR/UK data-subject rights explanation,
  downloadable from the decision page.
  A **subject-facing contest channel** now closes the loop: a subject's contest of an automated decline
  is logged (`POST /v1/decisions/{id}/contest`, by the channel they used) and stays open until a human
  review of that decision resolves it — surfaced as an "awaiting review" queue on the compliance
  dashboard, so a contest is tracked from receipt to outcome, not just recorded after the fact.
  A workspace now records its **applicable regimes** (`platform/jurisdiction`: eu/uk/us), so the
  decision explanation cites the law that applies rather than hedging across all three — a UK-only
  workspace drops the EU Regulation; a US-only workspace cites the Equal Credit Opportunity Act, not
  Article 22. Editable on the compliance dashboard (admin), defaulting to all three when unset.
  **Byte-level adverse-action artifact retention now ships:** issuance records the exact rendered
  Markdown in the append-only event, projects it into a dedicated artifact store, verifies it against
  its SHA-256 hash on download, and reproduces it unchanged after settings/template changes or a full
  replay. Still open: the ops-heavy scale tail (log compaction now shipped — a segmented,
  self-archiving WAL with soak + store-failure coverage; backup automation still open).

The former parallel non-code track is now specified with release evidence in §8b.9; it remains
independent of, and just as necessary as, the implementation PR sequence.

## 9. Scope boundaries (current)

The original MVP non-goals have mostly been overtaken — **SSO (OIDC + SAML) and SCIM shipped** in the
enterprise track. OIDC sessions retain the verified issuer, subject, session identifier, and raw ID
token server-side. Intraktible completes OpenID Connect RP-Initiated Logout through the provider's
discovered end-session endpoint, accepts signed Back-Channel Logout tokens to revoke one provider
session or every Intraktible session for a subject, and accepts issuer-bound Front-Channel Logout
notifications for the exact provider session. Local revocation and browser-cookie expiry were completed
before provider navigation, including provider-metadata and durable-store error paths, so every UI
logout surface failed closed and returned to Intraktible's durable, app-local signed-out page. That
accessible light/dark page never initiated login automatically and offered an explicit Shauth recovery
action when Shauth was configured. OIDC identity mapping resolved a verified email through the standard
UserInfo endpoint when the ID token omitted it. A real Shauth + Ory Hydra + PostgreSQL + browser CI stack,
pinned to merged Shauth commit `6d06480e2ec26250c12b88af3e36ab83787f6cf3`, covered direct protected
entry, direct and catalog silent SSO without a second credential prompt, `client_secret_post`, identity
display, every Intraktible logout surface returning to Intraktible, explicit recovery, provider-global
logout, local revocation, verified Back-Channel Logout delivery from the exact production provider
artifact, strict missing/expired `exp` rejection, stale-cookie rejection by identity and core APIs, and
protected-route re-entry. The gate also proved that a separately generated non-authentic sentinel was
rejected by Intraktible's JSON password, API-key exchange, HTTP Basic, Bearer, and `X-Api-Key` surfaces
without creating a session. A browser request boundary allowed the real validator password only in the
exact form-encoded Shauth `POST /login`, rejected mutated methods, paths, origins, headers, and return
coordinates, and required exactly one approved submission per credentialed login. An empty inherited
environment plus live-process inspection kept the validator variable names and password out of the
application process. Login and logout tokens were accepted
only from the exact configured issuer, including when a near-match issuer used the trusted signing key.
Shauth's deployment-neutral validator used `/auth/validation` as Intraktible's cookie-only authenticated
validation URL and `/v1/auth/signed-out` as its persistent signed-out URL. The validation page exposed
the verified OpenID Connect username and email, normalized `developer`/`admin` role, ordinary logout,
and the exact immutable `APPLICATION_RELEASE_REVISION` through stable accessible markers. Anonymous,
API-key, HTTP Basic, and Bearer entry failed closed to the app-local recovery page. The Amazon Elastic
Container Service Terraform module required the generic release coordinate, validated it as a 12–64
character lowercase hexadecimal commit or SHA-256 digest, and injected it into both application task
shapes; neither cloud-orchestrator detail nor validator credentials entered the app. `/version` exposed
the same generic revision with the binary's immutable build revision as its fallback. Still
**out of scope** (and why): multi-tenant billing (not a product
goal); exact API/UX parity with any commercial product (we are the open-source, self-hostable analog,
not a clone). Formerly a non-goal, now **moved into the §8b forward roadmap**: real data-connector
breadth (Phase 9), production HA/clustering + scale-out correctness (Phase 8), and ONNX model serving at
scale (a Phase 8/9 candidate). The **non-code work** (SOC 2 / ISO, pen tests, bureau relationships,
model-validation staffing) is out of scope _for code_ but tracked as the parallel track.

## 10. Open questions

All original backbone questions are **resolved and shipped**: log storage — a pluggable interface with
file WAL / SQLite / Postgres / NATS JetStream backends (no single BadgerDB bet needed); code-node
language — Starlark for the Code node + expr-lang for expressions (JS/WASM never required). Also locked
during requirements gathering and unchanged: Go + SvelteKit/Svelte Flow, pure-Go embedded event
backbone, hybrid ES purity, build sequence (core→engine→cases→context→agents), multi-tenancy
(org+workspace from day 1), web delivery embedded in the Go binary, API keys + session auth, pluggable
AI provider. New open questions now live in the §8b roadmap phases, not here.

## 11. GitHub Pages demo reliability

The browser-hosted WebAssembly backend used a schema-aware local AI provider for its public,
network-independent product journeys. Structured agent completions honored JSON Schema constants,
enums, primitive types, nested objects, arrays, and numeric bounds before the real agent runtime
validated and recorded them. The GitHub Pages browser gate exercised the Collections Hardship Program
explicitly, including its numeric structured-agent output, and required both a rendered preview verdict
and the absence of an execution error.
