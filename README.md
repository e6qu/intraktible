# intraktible

Open-source MVPs of a commercial **Agentic Decision Platform**, in four components:

- **decision-engine/** — drag-and-drop builder + execution runtime for versioned decision flows
- **case-manager/** — human-review queues & dashboards for escalated decisions
- **context-layer/** — entities/events/features data model + connectors
- **agent-manager/** — configure/run/monitor LLM task-agents inside flows

**Stack:** Go (functional core / imperative shell) backend · SvelteKit + Svelte Flow frontend ·
pure-Go embedded append-only **event log** with **hybrid event sourcing** + JSONB projections
(pluggable SQLite/Postgres) · modular monolith that also splits into services · pluggable AI provider.

See **[PLAN.md](PLAN.md)** for the full architecture and roadmap. Status: **MVP complete** — all four
components plus the shared core are built (Phases 0–5: PLAN §8), with replay/rollback operator tooling
and a split-services profile. Start at **[AGENTS.md](AGENTS.md)**; a runnable end-to-end walkthrough is
in **[docs/EXAMPLE.md](docs/EXAMPLE.md)**. Post-MVP backlog: **[BUGS.md](BUGS.md)**.

Run it: `go run ./cmd/intraktible serve` then open http://localhost:8080 (dev key `dev-sandbox-key`),
or `./examples/demo.sh`. Inspect/rebuild the log with `intraktible log` / `intraktible replay`.

## License
**AGPL-3.0-or-later** — see [LICENSE](LICENSE). Every dependency must be AGPL-compatible
(MIT/BSD/ISC/Apache-2.0/MPL-2.0 or compatible copyleft); **SSPL, BUSL/BSL, Elastic License, Commons
Clause, and GPL-2.0-only are disallowed**, enforced in CI (`go-licenses` + `license-checker`).
Policy & vetted-deps table: [docs/LICENSING.md](docs/LICENSING.md). As a network service, AGPL §13
applies — hosted instances must offer their source.

> Independent reimplementation of the *concepts* — not affiliated with or derived from any vendor's
> code/assets. Research basis is in the parent directory (`../specs/`, `../docs/`).
