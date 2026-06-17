# AGENTS.md — start here

Entry point for any agent (or human) picking up **intraktible**: an open-source, AGPL-3.0-or-later
reimplementation of a commercial Agentic Decision Platform.

## Where to read, in order
1. **[PLAN.md](PLAN.md)** — architecture, locked decisions, component scope, phased roadmap. Source of truth.
2. **[docs/LICENSING.md](docs/LICENSING.md)** — AGPL policy + the dependency allow/deny rules (CI-enforced).
3. Component subplans: [decision-engine](decision-engine/README.md) · [case-manager](case-manager/README.md) · [context-layer](context-layer/README.md) · [agent-manager](agent-manager/README.md) · shared [platform](platform/README.md).
4. Research basis (why the design looks like this): `../specs/openapi-current.yaml`, `../ENDPOINTS.md`, `../docs/`. (That parent tree is research only — **do not** mix it into this repo.)

## Status
**MVP roadmap complete — Phases 0–5 all DONE** (shared core, Decision Engine, Case Manager, Context
Layer, Agent Manager, and Harden: replay/rollback tooling + split-services profile + a worked example).
See a full end-to-end walkthrough in [docs/EXAMPLE.md](docs/EXAMPLE.md) (runnable: [examples/demo.sh](examples/demo.sh)).
Post-MVP backlog is tracked in [BUGS.md](BUGS.md).
Roadmap & exit criteria: [PLAN.md §8](PLAN.md#8-phased-roadmap); deferrals tracked in [BUGS.md](BUGS.md).
Working today: `platform/{eventlog,store,projection,identity,auth,httpx,ai,web}` + the `hello`
slice; and the **Decision Engine** — flow model + versioning, a deterministic execution runtime
(Input/Assignment/Rule/Split/Scorecard/Decision Table/2D Matrix/Code/Reason/Output; the Reason node
emits structured adverse-action reason codes lifted first-class onto the decision; expr-lang for
expressions, Starlark for the Code node), the `…/{env}/decide` API with **per-environment version
pinning + A/B (champion/challenger) routing**, **batch decisioning** (`…/{env}/decide/batch` runs a
dataset of inputs through the recorded decide path — each row a real decision, capped at 500),
**policies** (`decision-engine/policy` — a first-class versioned artifact mapping a flow's output to a
disposition `approve`/`decline`/`refer` via expr bands; the decide path applies the bound policy and
records the disposition on the decision — the shared brain for real-time/bulk/pre-approval decisioning),
**pre-approvals** (`decision-engine/preapproval` — durable, time-boxed grants per entity that the decide
path honors instantly: a pre-approved entity is completed from the grant's terms, skipping the flow,
recorded with `preapproval_id`; `…/{env}/preapprove/batch` promotes a whole population — every row the
bound policy approves becomes a grant keyed by a row field), decision history, **analytics-lite** (per-flow metrics with champion/challenger breakdown), **monitors** (`decision-engine/monitor` — thresholds over a flow's live metrics: failure/refer/automation/approve/decline rate, latency, volume, and **distribution drift** vs a captured baseline, evaluated firing/ok at read time; an optional `monitor.Scheduler` sweeps on `INTRAKTIBLE_MONITOR_INTERVAL` and notifies on the ok→firing edge), and **notifications** (`decision-engine/notify` — webhook subscriptions + SSRF-safe delivery; a monitor `…/monitors/check` pushes firing rules to active webhooks and records each delivery) — all
command→event→projection→API, durable & replayable.
The **Svelte Flow builder UI** (`web/src/routes/engine`) lists/creates flows and edits a flow's graph
(add nodes from a palette, wire edges, edit per-node config via structured panels for common types or
raw JSON, publish a new version — with backend
validation surfaced), renders it on a canvas (auto-layout), runs inline test decisions, and manages
**deployment + maker-checker + promotion** (sandbox→staging→production envs, a `…/promote {from,to}` that ships the live version up the chain — direct into non-prod, maker-checker request into prod; live-version-per-env badges, deploy-to-sandbox, propose-for-production,
a pending-approvals queue with approve/reject — self-approval refused — and A/B challenger %), and a
**version-diff** panel (client-side structural compare of any two published versions: added/removed/
changed nodes + added/removed edges). A flow version **exports** to Mermaid / BPMN / Graphviz DOT /
round-trippable JSON, and a flow JSON **imports** straight back onto the canvas (paste or upload) to
publish into any flow — so flows move between environments and version control.
The **Case Manager** (`case-manager/`) opens cases — manually or **escalated from a decision flow's
`manual_review` node** (cross-component via the event log, linked by `source_decision_id`) — with
assignment / status / notes, a queue with filters, a per-case audit log built from events, **SLA
tracking** (days-left + on_track/due_soon/overdue computed at the read boundary so the projection
stays clock-free) and a **queue summary** roll-up (`GET /v1/cases/summary`); its **dashboard UI**
(`web/src/routes/cases`) has the queue (filters + summary banner + per-row days-left + an SLA-sweep
trigger) and a case-detail view with those actions.
The **Context Layer** (`context-layer/`) records **custom entities** (dynamic JSONB, keyed by
type+id, re-records patch via top-level attribute merge) and **custom events** about them (per-entity
event log + an event count; an event auto-creates a shell entity; `occurred_at` is a recorded effect),
and runs a **feature engine** — windowed `count`/`sum` aggregates over an entity type's event stream
(`pure domain.Compute`, computed read-time against now so the log stays clock-free); all
command→event→projection→API. Decisions can carry an `{entity_type, entity_id}` ref so the engine
folds that entity's features into the input under `features.*` (read by Rule/Split expressions,
recorded in `DecisionStarted`); the engine reaches the Context Layer through a `FeatureProvider` port
+ adapter wired at the composition root (so the earlier-built engine never imports it). It also has a
**connector** subsystem — a `Connect` interface + reference connectors (an arbitrary-HTTP one and a
deterministic mock bureau) — where invoking a connector is an effect recorded as a `ConnectorFetched`
event (so replay reads the stored response, never a re-fetch). Its **Context data UI** (`web/src/routes/data`)
lists/defines connectors and features and browses entities + their per-entity event timelines (so the
data a flow's Connect/Rule nodes depend on can be authored in the product, not just via the API).
The **Agent Manager** (`agent-manager/`) defines **agents** — configs over the pluggable AI provider
(`platform/ai`: system prompt, model, optional structured-output schema, tool set) — and **runs**
them: the provider call is an effect captured in an `AgentRunRecorded` event (so replay reads the
recorded output, not a re-call), with an agent registry + run-log/monitoring projection. An OpenAI-compatible HTTP
provider ships (`ai.NewHTTP`, enabled via `INTRAKTIBLE_AI_BASE_URL`/`_API_KEY`/`_MODEL`); the Stub is the default fallback. A flow's **AI node** runs an agent during a
decision (shell pre-resolves it via an `AgentProvider` port + `agents.Provider` adapter, injecting the
output under `ai.<output>`) — the same one-way wiring as features/connectors. **Human-in-the-loop**:
escalating a run opens a **Case Manager** case (the Agent Manager, built later, emits the Case
Manager's own `ReviewRequested` event the `cases` projector already consumes — one-way direction), and
`GET /v1/agent-runs/summary` gives run monitoring. Its **UI** (`web/src/routes/agents`) lists/defines
agents — the define form covers provider, model, system prompt, **a tool set, and a structured-output
schema** (so tool-calling / structured agents are authorable, not just plain-text ones; the list shows
per-agent capability badges) — and, per agent, runs it, shows the run log, and escalates a run.
Run it: `go run ./cmd/intraktible serve` then open http://localhost:8080 (dev key `dev-sandbox-key`);
for UI dev use `make dev` (Vite + Go API). Phase 1 deferrals (CEL, builder UI polish, …) and other
limitations are tracked in [BUGS.md](BUGS.md).
All five phases are built, plus a post-MVP hardening pass that closed almost the whole backlog: durable
SQLite **and Postgres** projection stores (`--store=sqlite|postgres`), a shared SQLite event log
(`--log=sqlite`) and a streaming offset-indexed file WAL, real OpenAI-compatible AI providers with agent
**tool-calling** and **async/queued runs**, full builder panels + drag-to-connect, an SSRF egress policy,
a SQL connector, a recursive JSON-Schema validator, and pushed SLA-breach events. The small tail left in
[BUGS.md](BUGS.md): incremental resume-from-Head (D21b), the closed-by-decision items (CEL, batched
events), and the §9 non-goals.
An **enterprise-readiness track** (tracked in [docs/ENTERPRISE.md](docs/ENTERPRISE.md)) then began on
top of the MVP: **RBAC** (a `platform/auth` role hierarchy viewer→operator→editor→approver→admin with
per-request authorization in `platform/httpx`), **maker-checker / four-eyes** production deploys
(propose-by-one + approve-by-a-different-user via `…/deployment-requests` + `…/approve`, recorded on the
flow), **backtesting** — `POST /v1/flows/{flow_id}/backtest` (`decision-engine/backtest`, pure)
replays a dataset through a published version and optionally diffs two versions over `domain.Execute`
only (no recorded decision, no I/O), surfaced in the builder as a panel that flags the changed records —
and the **immutable audit surface** — `GET /v1/audit` (`platform/audit`), a tenant-scoped, filterable
(stream/actor/type/resource/time-range), CSV-exportable read straight over the append-only event log,
admin-gated and surfaced as an Audit log UI page (the data was always in the log; this makes it
first-class instead of operator-CLI-only) whose filters are **URL-synced** — a filtered forensic view is
deep-linkable, bookmarkable, and back/forward-navigable. **Comment threads** (`platform/comments`) are a
general capability: a chronological discussion attached to any subject (`subject_type`+`subject_id`) via
`GET/POST /v1/comments/{type}/{id}`, surfaced on the workflow items that get approved/rejected/promoted
(deployment requests), on flows, policies, and decisions — so every reviewable thing carries an
explanation trail (a reusable `CommentThread.svelte` drops onto any subject). **PII
masking** (`platform/privacy`) adds a
per-workspace sensitive-field list (admin-gated to change) whose values are redacted in decision
input/output, node traces, and exports at the read boundary — the raw event log stays the source of
truth; managed from the Audit page.

## The design in one breath
Go backend (**functional core / imperative shell**) + **SvelteKit + Svelte Flow** UI embedded in the
binary — a shared layout with **light/dark theming** (toggleable, persisted, OS-default), an inline-SVG
icon set, and flow **export** (Mermaid / BPMN / Graphviz DOT / round-trippable JSON) from the builder. The UI is **persona-aware**:
a client-side "view-as" switch (anyone can flip it — a presentation preference, *not* RBAC) re-skins and
re-prioritises the whole app for three viewers — **Builder** (a dense monospace command-deck for the
developer/maintainer), **Operator** (calm KPI mission-control for the risk/ops manager), and **Showcase**
(an editorial serif story for stakeholders). Persona drives accent, type system (self-hosted IBM Plex
Sans/Mono + Fraunces, OFL, vendored — no runtime CDN), and density via a `data-persona` attribute,
orthogonal to `data-theme`; the landing page is a different dashboard per persona, all over the same
data. The **Admin surface** (the audit ledger) is exempt — a fixed, canonical slate-indigo identity that
reads the same for everyone regardless of persona. A **⌘K command palette** (`lib/CommandPalette.svelte`)
jumps to any page, switches persona/theme, and **searches the tenant's flows/agents/cases by name** to
open them — all from the keyboard; a **`?` shortcuts overlay** documents it alongside `t` (theme) and
`g`-then-key navigation (`lib/ShortcutsOverlay.svelte`); and developer IDs (e.g. the decision id)
are **click-to-copy** (`lib/Copyable.svelte`). Timestamps render as live **relative times** ("2m ago",
absolute on hover) via a single shared clock (`lib/time.ts` + `lib/RelativeTime.svelte`). List pages share
a tokenized table style with designed **empty states** and **loading skeletons**; async actions show
in-flight (disabled) states to prevent
double-submit; the flow list surfaces per-environment deployment status. A **pure-Go embedded
append-only event log** is the backbone; **hybrid event sourcing**
(events are truth, **JSONB projections** are rebuilt views) gives **perfect replay + log-based
rollback**. **Modular monolith** that also splits into services. **Org+workspace scoped** from day 1.
Pluggable storage (SQLite/Postgres) and pluggable AI provider. Details: [PLAN.md §3](PLAN.md#3-architecture).

## Non-negotiable conventions
- **Functional core / imperative shell**: pure logic in `domain/`; I/O only in `service/`.
- **Deterministic core** (prereq for replay): no wall-clock/random in core except via injected, recorded effects.
- **Fail loudly** — no silent fallbacks / empty catches / "log & continue" in logic (network retries are fine).
- **License**: `AGPL-3.0-or-later`; SPDX header on every file (`SPDX-License-Identifier: AGPL-3.0-or-later`); deps must pass the license gate ([docs/LICENSING.md](docs/LICENSING.md)).
- **Docs cadence**: update [PLAN.md](PLAN.md) and [BUGS.md](BUGS.md) in the **same PR** that ends a phase.
- **No phase/issue refs in source** — keep the "why" in commit messages, not code comments.
- Strict linting + **dead-code** + **copy-paste** detection are CI gates.

## Per-component layout (every component)
`domain/` (pure) · `events/` (event payloads) · `command/` (validate→emit) · `projection/` (events→JSONB) · `service/` (HTTP + wiring).

## Build / run
- `make build` — Go binary (embeds whatever is in `platform/web/assets`: the committed placeholder, or
  the real UI if `make web` ran); `make web` — build the SvelteKit UI + copy it into the embed dir;
  `make dist` — the full self-contained artifact (`web` + `build`). The binary serves the SPA with
  client-side-route fallback. `make check` — fast gate; `make ci` — full gate (everything CI runs).
- Run: `intraktible serve --modules=all` (monolith) or `--modules=decision-engine` (split). The
  projection store is `--store=memory` (default, ephemeral) or `--store=sqlite` (durable, persists to
  `<data-dir>/projections.db`); either way projections rebuild from the log on boot.
- Operate (Phase 5): `intraktible log` prints the event log (audit) + per-stream summary;
  `intraktible replay [--modules] [--as-of <seq>]` rebuilds projections from the log into a fresh
  store and reports the rebuilt collections — `--as-of` is a read-only **log-based rollback** to that
  seq (the append-only log is never mutated). `GET /healthz` reports projection health — 503
  `degraded` if a live-apply error stopped the consumer (so an orchestrator can restart the node).

## Testing & quality gates (enforced, not optional)
- **Test pyramid, per module:** pure **unit** tests (`domain/`, platform pkgs) → **integration**
  (command→event→projection→replay) → **API HTTP e2e** (`*_e2e_test.go` via the
  `platform/testutil.StartAPI` httptest harness) → **UI e2e** (`web/e2e/*.spec.ts`, Playwright over the
  real Go API + Vite). Shared Go test fixtures live in `internal/.../*test` and `platform/testutil`.
- **Pre-commit pipeline** ([`.pre-commit-config.yaml`](.pre-commit-config.yaml), framework:
  [pre-commit.com](https://pre-commit.com)) — run `pre-commit install` once. **Commit** stage:
  autoformat (gofmt/prettier), strict lint (golangci-lint / eslint), strict typecheck (go build /
  svelte-check), strict SAST (gosec / eslint-security), unit+integration+API-e2e tests. **Push**
  stage: race tests, dead-code, copy-paste, vuln, license, Playwright UI e2e. Hooks call the same
  `make` targets / npm scripts as CI, so local == CI. Go tooling excludes `web/node_modules`.

## Git / identity (this repo)
Author **Adrian Mârza**, committer email `2966430+e6qu@users.noreply.github.com`, pushes use the
**e6qu** SSH key (`core.sshCommand` is pinned to `~/.ssh/id_ed25519_e6qu`). No remote set yet.
