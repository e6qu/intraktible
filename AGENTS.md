# AGENTS.md — start here

Entry point for any agent (or human) picking up **intraktible**: an open-source, AGPL-3.0-or-later
reimplementation of a commercial Agentic Decision Platform.

## Where to read, in order
1. **[PLAN.md](PLAN.md)** — architecture, locked decisions, component scope, phased roadmap. Source of truth.
2. **[docs/LICENSING.md](docs/LICENSING.md)** — AGPL policy + the dependency allow/deny rules (CI-enforced).
3. Component subplans: [decision-engine](decision-engine/README.md) · [case-manager](case-manager/README.md) · [context-layer](context-layer/README.md) · [agent-manager](agent-manager/README.md) · shared [platform](platform/README.md).
4. Research basis (why the design looks like this): `../specs/openapi-current.yaml`, `../ENDPOINTS.md`, `../docs/`. (That parent tree is research only — **do not** mix it into this repo.)

## Status
**Phase 0 (shared core) DONE; Phase 1 (Decision Engine) next.** Roadmap & exit criteria:
[PLAN.md §8](PLAN.md#8-phased-roadmap); deferrals tracked in [BUGS.md](BUGS.md).
Working today: `platform/{eventlog,store,projection,identity,auth,httpx,ai,web}` + the `hello`
vertical slice (command→event→projection→API→UI, durable & replayable). Run it:
`go run ./cmd/intraktible serve` then open http://localhost:8080 (dev key `dev-sandbox-key`).
Build order remaining: Decision Engine → Case Manager → Context Layer → Agent Manager.

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
