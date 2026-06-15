# AGENTS.md — start here

Entry point for any agent (or human) picking up **intraktible**: an open-source, AGPL-3.0-or-later
reimplementation of a commercial Agentic Decision Platform.

## Where to read, in order
1. **[PLAN.md](PLAN.md)** — architecture, locked decisions, component scope, phased roadmap. Source of truth.
2. **[docs/LICENSING.md](docs/LICENSING.md)** — AGPL policy + the dependency allow/deny rules (CI-enforced).
3. Component subplans: [decision-engine](decision-engine/README.md) · [case-manager](case-manager/README.md) · [context-layer](context-layer/README.md) · [agent-manager](agent-manager/README.md) · shared [platform](platform/README.md).
4. Research basis (why the design looks like this): `../specs/openapi-current.yaml`, `../ENDPOINTS.md`, `../docs/`. (That parent tree is research only — **do not** mix it into this repo.)

## Status
**Phase 0 (shared core), Phase 1 (Decision Engine), Phase 2 (Case Manager), and Phase 3 (Context Layer) DONE. Phase 4 (Agent Manager) IN PROGRESS.**
Roadmap & exit criteria: [PLAN.md §8](PLAN.md#8-phased-roadmap); deferrals tracked in [BUGS.md](BUGS.md).
Working today: `platform/{eventlog,store,projection,identity,auth,httpx,ai,web}` + the `hello`
slice; and the **Decision Engine** — flow model + versioning, a deterministic execution runtime
(Input/Assignment/Rule/Split/Scorecard/Decision Table/2D Matrix/Code/Output; expr-lang for
expressions, Starlark for the Code node), the `…/{env}/decide` API with **per-environment version
pinning + A/B (champion/challenger) routing**, decision history, and **analytics-lite** (per-flow
metrics with champion/challenger breakdown) — all command→event→projection→API, durable & replayable.
The **Svelte Flow builder UI** (`web/src/routes/engine`) lists/creates flows and edits a flow's graph
(add nodes from a palette, wire edges, edit per-node config, publish a new version — with backend
validation surfaced), renders it on a canvas (auto-layout), and runs inline test decisions.
The **Case Manager** (`case-manager/`) opens cases — manually or **escalated from a decision flow's
`manual_review` node** (cross-component via the event log, linked by `source_decision_id`) — with
assignment / status / notes, a queue with filters, a per-case audit log built from events, **SLA
tracking** (days-left + on_track/due_soon/overdue computed at the read boundary so the projection
stays clock-free) and a **queue summary** roll-up (`GET /v1/cases/summary`); its **dashboard UI**
(`web/src/routes/cases`) has the queue (filters + summary banner + per-row days-left) and a
case-detail view with those actions.
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
event (so replay reads the stored response, never a re-fetch).
The **Agent Manager** (`agent-manager/`) defines **agents** — configs over the pluggable AI provider
(`platform/ai`: system prompt, model, optional structured-output schema, tool set) — and **runs**
them: the provider call is an effect captured in an `AgentRunRecorded` event (so replay reads the
recorded output, not a re-call), with an agent registry + run-log/monitoring projection. Only the Stub
provider is wired so far (real adapters tracked in BUGS). A flow's **AI node** runs an agent during a
decision (shell pre-resolves it via an `AgentProvider` port + `agents.Provider` adapter, injecting the
output under `ai.<output>`) — the same one-way wiring as features/connectors. **Human-in-the-loop**:
escalating a run opens a **Case Manager** case (the Agent Manager, built later, emits the Case
Manager's own `ReviewRequested` event the `cases` projector already consumes — one-way direction), and
`GET /v1/agent-runs/summary` gives run monitoring.
Run it: `go run ./cmd/intraktible serve` then open http://localhost:8080 (dev key `dev-sandbox-key`);
for UI dev use `make dev` (Vite + Go API). Phase 1 deferrals (CEL, builder UI polish, …) and other
limitations are tracked in [BUGS.md](BUGS.md).
Build order from here: finish Agent Manager → Harden.

## The design in one breath
Go backend (**functional core / imperative shell**) + **SvelteKit + Svelte Flow** UI embedded in the
binary. A **pure-Go embedded append-only event log** is the backbone; **hybrid event sourcing**
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
- `make build` — embed UI, single binary; `make check` — fast gate (fmt + vet + typecheck + tests);
  `make ci` — full gate (everything CI runs); `make web` — build + embed the SvelteKit UI.
- Run: `intraktible serve --modules=all` (monolith) or `--modules=decision-engine` (split).

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
