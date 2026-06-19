# platform — shared core & infrastructure

New here? Start at [../AGENTS.md](../AGENTS.md). Shared building blocks for all four components
(see [../PLAN.md](../PLAN.md) §5):
`eventlog/` (pure-Go append-only log + bus) · `store/` (SQLite/Postgres JSONB adapters) ·
`projection/` (runtime + rebuild) · `schema/` (dynamic JSON Schema) · `ai/` (pluggable provider) ·
`httpx/` (server/routing/middleware) · `auth/` (static + managed API keys, sessions) · `telemetry/`.
