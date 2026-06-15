# Decision Engine

A component of **intraktible** (see [../PLAN.md](../PLAN.md) §4). New here? Start at [../AGENTS.md](../AGENTS.md).

Layout (functional core / imperative shell):
```
domain/      # pure types + logic (no I/O): graph validation, etag, command validation
events/      # event payloads + flow data model (Node/Edge/Graph, NodeType palette)
command/     # validate (pure) -> emit events (slug uniqueness + version numbering folded from the log)
flows/       # events -> JSONB read model (flow registry: metadata + published versions)
service/     # HTTP handlers + wiring (imperative shell)
```

Status: **in progress (Phase 1).**

Done — flow model + versioning (vertical slice, command→event→projection→API, durable & replayable):
- Flow = versioned DAG of typed nodes/edges; each `FlowVersionPublished` is immutable, numbered
  monotonically (1, 2, …) and stamped with a content `etag` over `(graph, input_schema)`.
- `ValidateGraph` fails loudly: unique node IDs of known types, exactly one Input + ≥1 Output,
  edges reference existing distinct nodes, acyclic (Kahn).
- HTTP (under `/v1/`, X-Api-Key / session auth, org+workspace scoped):
  - `POST /v1/flows` — create `{slug, name}` → `{flow_id}`
  - `POST /v1/flows/{flow_id}/versions` — publish `{graph, input_schema}` → `{version, etag}`
  - `GET /v1/flows` · `GET /v1/flows/{flow_id}` — registry read model
- Run it: `intraktible serve --modules=decision-engine`.

Next in Phase 1 (see [../PLAN.md](../PLAN.md) §4.1, §8): node engines (CEL/expr/Starlark),
execution runtime emitting the decision event stream, the `…/{env}/decide` API, decision history,
Svelte Flow builder + test runs, A/B + analytics-lite.
