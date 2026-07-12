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

**MVP roadmap complete (Phases 0–5), plus a large post-MVP hardening + enterprise track.** The
per-slice narrative that used to live here has been archived — the authoritative slice-by-slice history
(every PR, every audit round, every deferred item) is in **[BUGS.md](BUGS.md)**. What that track
delivered, by theme:

- **Durability & scale-out backbone:** durable SQLite + **Postgres** projection stores; a streaming
  (offset-indexed) file WAL; a shared SQLite event log and a **NATS JetStream** networked log for the
  split-services profile; Postgres `LISTEN/NOTIFY` fast path.
- **Governance & change control:** RBAC (viewer→operator→editor→approver→admin); **four-eyes
  maker-checker** on flow deploys with pre-approval binding + environment-scope gating; flow
  **assertions + promotion gates**; **shadow deploys**; instant rollback; comment threads + @-mention
  notifications inbox on every reviewable subject.
- **Enterprise identity:** OIDC SSO (Google, Cognito, generic) + **SAML 2.0**; **SCIM** user/group
  provisioning + deprovisioning honored by live sessions.
- **Decisioning depth:** decision-table hit policies + aggregators; **ML model hosting**
  (logistic/GBM/expression/external) with a **Predict node**; an external-decision compatibility API;
  champion/challenger + monitors (**PSI drift**, covariate drift, actuals reconciliation) + SLOs.
- **Model-risk & governance packaging:** **SR 11-7 / SS1/23 model inventory** (`mrm/`) across flows,
  models, and agents; AI/ML governance — agent registry/versioning, offline eval, guardrails, cost
  attribution; structured **reason codes** end-to-end.
- **Compliance & data protection:** AES-256-GCM at rest with AAD binding, **crypto-shred erasure**,
  **PII masking** at the read boundary, OTel tracing, an SSRF egress guard, and a full audit surface.
- **Persona-adaptive UI:** per-persona surfaces (Builder, Operator, Risk, Team Manager,
  Experimentation, Executive, Evaluator) over an API-first design — OpenAPI 3.1 + Go/TypeScript SDKs.
- **Hardening:** eleven+ multi-agent audit rounds (correctness, security, fake-hunting,
  accessibility/WCAG-AA-in-CI, live-UI walkthroughs) — see the R-/DR-/BF- blocks in `BUGS.md`.

---

## 8b. Forward roadmap — the regulated-lending & production track

We are far from a production release. What exists is a decision engine with a governance layer
(deterministic replay, audit/lineage, RBAC, four-eyes on flows, drift, crypto-shred erasure). A
multi-agent review and a competitor comparison (**[docs/COMPETITIVE.md](docs/COMPETITIVE.md)**) point to
what is not built: fair-lending testing, adverse-action notices, and independent model validation are
absent, and nothing has been run at scale (single-node projection; no load or chaos evidence). Nothing
here is a claim that a competitor is beaten — only a list of gaps to work through. The roadmap orders
them hardest-blocker-first; each phase is a direction, not a committed date.

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
  reference; independent model validation of the fair-lending model itself lands in Phase 7.
- **Phase 7 — Model governance parity — ✅ DONE.** Models now carry a **version** (each redefine bumps
  it) and a **four-eyes approval** (`ModelApprovalRequested/Approved/Rejected`): a maker requests, and a
  checker who is neither the requester nor the version's author approves — a redefine invalidates a
  prior approval, the same "changed logic, re-review" rule flows follow. Enforcement mirrors flows:
  **outside the sandbox, a Predict node refuses a model whose current version is not approved**.
  **Validation evidence** (`ModelValidationRecorded`: dataset, metrics, validator, notes, pass/fail)
  attaches to a version — what a checker reviews. The **MRM inventory** flags an unapproved model and a
  model with no validation evidence as governance gaps (they fire like any other MRM issue), and the
  models page carries the approval status, request/approve/reject, and the validation log. The demo seed
  runs every model through validation + approval, so its production decisions serve approved models.
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
  against a live Postgres — no longer skipped everywhere) landed too. _Still open (the ops-heavy tail):_
  load + chaos tests, and compaction/archival/backup automation.
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
  *wrong* basis for credit decisioning (power imbalance → not freely given; the ICO's own worked example
  is a credit-reference pull) — so a grant records the Art. 6 basis (contract/legitimate_interest for
  decisioning, not consent) plus optional **`Evidence`**: how it was obtained (a controlled vocabulary),
  a reference to the signed artifact in the controller's own store, a **content hash** for tamper-
  evidence, and the **notice version** shown. The subject's data page lets an operator attach a file that
  is **hashed in the browser (SHA-256)** — only the fingerprint + name are stored, the document's bytes
  never leave the tenant (data residency). The demo seed uses the correct basis and a worked evidence
  record for applicants. **Adverse-action issuance is now a durable record**, the mirror of consent
  (consent gates the data *pull*; adverse action governs a decline's *output*). The stateless ECOA
  notice render became an auditable issuance — `POST /v1/decisions/{id}/adverse-action/issue` records
  who served the notice, when, by what delivery method, citing which principal reasons, plus a SHA-256
  hash of the exact document (the proof ECOA/Reg B expects within 30 days). The notice gained the
  **FCRA §615(a)** disclosures (consumer-reporting-agency identity, "the CRA did not make the decision",
  right to a free report + to dispute) for report-based declines, failing loud if the CRA is
  unconfigured. A **pending-notices work queue** (`GET /v1/adverse-actions`) surfaces declines awaiting
  a notice with their age (the 30-day clock); a compliance operator issues from the decision page, and
  the demo seeds both issued and pending notices. _Still open:_ GLBA privacy opt-out disclosure; Art. 22
  automated-decision safeguards and the UK 22A–22D split; byte-level WORM artifact storage;
  retention-clock enforcement.

**Parallel non-code track (organisational, not code):** SOC 2 Type II, ISO 27001, independent
penetration testing, data-provider commercial relationships, model-validation staffing, and reference
deployments. Code does not produce these, and they gate a regulated rollout as much as any missing
feature.

## 9. Scope boundaries (current)
The original MVP non-goals have mostly been overtaken — **SSO (OIDC + SAML) and SCIM shipped** in the
enterprise track. Still **out of scope** (and why): multi-tenant billing (not a product
goal); exact API/UX parity with any commercial product (we are the open-source, self-hostable analog,
not a clone). Formerly a non-goal, now **moved into the §8b forward roadmap**: real data-connector
breadth (Phase 9), production HA/clustering + scale-out correctness (Phase 8), and ONNX model serving at
scale (a Phase 8/9 candidate). The **non-code work** (SOC 2 / ISO, pen tests, bureau relationships,
model-validation staffing) is out of scope *for code* but tracked as the parallel track.

## 10. Open questions
All original backbone questions are **resolved and shipped**: log storage — a pluggable interface with
file WAL / SQLite / Postgres / NATS JetStream backends (no single BadgerDB bet needed); code-node
language — Starlark for the Code node + expr-lang for expressions (JS/WASM never required). Also locked
during requirements gathering and unchanged: Go + SvelteKit/Svelte Flow, pure-Go embedded event
backbone, hybrid ES purity, build sequence (core→engine→cases→context→agents), multi-tenancy
(org+workspace from day 1), web delivery embedded in the Go binary, API keys + session auth, pluggable
AI provider. New open questions now live in the §8b roadmap phases, not here.
