# Decision Engine

A component of **intraktible** (see [../PLAN.md](../PLAN.md) §4). New here? Start at [../AGENTS.md](../AGENTS.md).

Layout (functional core / imperative shell):
```
domain/      # pure types + logic (no I/O)
events/      # event payload types
command/     # validate (pure) -> emit events
projection/  # events -> JSONB read models
service/     # HTTP handlers + wiring (imperative shell)
```
Status: planning — not yet implemented.
