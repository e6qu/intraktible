# AGENTS.md — start here

Entry point for any agent (or human) picking up **intraktible**: an open-source, AGPL-3.0-or-later
reimplementation of a commercial Agentic Decision Platform.

## Where to read, in order
1. **[PLAN.md](PLAN.md)** — architecture, locked decisions, component scope, phased roadmap. Source of truth.
2. **[docs/LICENSING.md](docs/LICENSING.md)** — AGPL policy + the dependency allow/deny rules (CI-enforced).
3. Component subplans: [decision-engine](decision-engine/README.md) · [case-manager](case-manager/README.md) · [context-layer](context-layer/README.md) · [agent-manager](agent-manager/README.md) · shared [platform](platform/README.md).
4. Research basis (why the design looks like this): `../specs/openapi-current.yaml`, `../ENDPOINTS.md`, `../docs/`. (That parent tree is research only — **do not** mix it into this repo.)

## Status
**Where the product is:** the MVP (Phases 0–5) and a large post-MVP enterprise track are **DONE**.
intraktible is an open-source, self-hostable, event-sourced **decision engine with a governance layer** —
a Decision Engine (flow builder + deterministic execution + ML Predict node + champion/challenger),
Case Manager, Context Layer (entities/events/features/connectors), and Agent Manager, over a shared
`platform/` (pluggable event log: file WAL / SQLite / Postgres / NATS; doc-store projections; auth/RBAC;
audit; OIDC+SAML SSO + SCIM; AES-256-GCM at rest + crypto-shred erasure; PII masking; OTel). Governance:
RBAC, **four-eyes maker-checker on flows**, promotion gates, shadow deploys, drift monitoring (PSI +
covariate + actuals), an SR 11-7 model inventory (`mrm/`), comment threads + a notifications inbox.

**The demo is the real backend:** the hosted demo boots the actual Go backend compiled to **wasm**
(`cmd/intraktible-wasm` in a worker; `web/src/lib/backend/` is the transport layer) — there is no
TypeScript mock. The seeded history (`cmd/intraktible-seed` → `web/static/demo-seed.json`, regenerate
with `make demo-seed`) is a real event log that replays through `server.New` at boot. Every command
handler takes a `WithNow` clock override for deterministic tests.

**History & detail** live where they belong — do not re-narrate them here:
- **[PLAN.md](PLAN.md) §8** — themed summary of everything delivered (MVP + enterprise track).
- **[PLAN.md](PLAN.md) §8b** — the **forward roadmap** (fair lending & adverse action → model-governance
  parity → production hardening at scale → connector resilience → command-path perf → regulatory data
  lifecycle) plus the parallel non-code track (SOC 2 / ISO, pen tests, bureau relationships).
- **[docs/COMPETITIVE.md](docs/COMPETITIVE.md)** — feature-by-feature vs Taktile / Alloy / Zest +
  the open-source landscape and where the gap is. Competitor entries are vendor claims, not tested.
- **[BUGS.md](BUGS.md)** — the authoritative slice-by-slice log: every post-MVP feature round, all 11+
  multi-agent audit rounds (correctness, security, fake-hunting, WCAG-AA-in-CI, live-UI walkthroughs),
  and every deferred item, block by block.
- **[docs/ENTERPRISE.md](docs/ENTERPRISE.md)** — the enterprise-readiness gap analysis.

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
See **[docs/CONVENTIONS.md](docs/CONVENTIONS.md)** for how we express things (named-type
enums + the wire-string boundary, when to use `platform/mo` vs idiomatic `(T, ok)`,
`identity.New` at external-input boundaries, publish-time flow validation, the projection
store contract, and the items deliberately left undone so they aren't "finished" by mistake).
- **Functional core / imperative shell**: pure logic in `domain/`; I/O only in `service/`.
- **Deterministic core** (prereq for replay): no wall-clock/random in core except via injected, recorded effects.
- **Fail loudly** — no silent fallbacks / empty catches / "log & continue" in logic (network retries are fine).
- **License**: `AGPL-3.0-or-later`; SPDX header on every file (`SPDX-License-Identifier: AGPL-3.0-or-later`); deps must pass the license gate ([docs/LICENSING.md](docs/LICENSING.md)).
- **Announce external dependencies first**: before adding or replacing any library, runtime, build tool, service, or container-image dependency, tell the user what is proposed, why it is needed, who owns it, its license and security implications, and the in-repo alternative. Do not change dependencies until that announcement has been made.
- **One open PR at a time**: never keep more than one pull request open on this repo simultaneously. Land (or close) the current PR before opening the next; serialize work into a single review queue.
- **Remote state is authoritative**: before editing or pushing, fetch the freshest `origin/main` and any existing remote PR head, compare them deliberately with the local branch and worktree, and reconcile or rebase without discarding uncommitted work. Never assume a local branch is current.
- **Fat PRs over anemic ones**: prefer one large PR that bundles substantial, related work over a trail of tiny PRs — explicitly fine even against the usual "small, focused PR" norm. Fold incidental changes (CI tweaks, doc lines, drive-by fixes) into the next substantial PR rather than opening a PR just for them.
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
**e6qu** SSH key (`core.sshCommand` is pinned to `~/.ssh/id_ed25519_e6qu`). Remote: `origin`
→ github.com/e6qu/intraktible; work lands via PRs to `main` (one open PR at a time).
