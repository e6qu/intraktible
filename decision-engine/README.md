# Decision Engine

A component of **intraktible** (see [../PLAN.md](../PLAN.md) §4). New here? Start at [../AGENTS.md](../AGENTS.md).

Layout (functional core / imperative shell):
```
domain/      # pure types + logic (no I/O): graph validation, etag, deterministic execution
events/      # event payloads: flow data model + the decision run stream
command/     # validate (pure) -> emit events; decide loads a version, runs the core, emits the run
flows/       # events -> JSONB read model (flow registry: metadata + published versions)
history/     # events -> JSONB read model (decision history: request, node trace, response)
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

Done — execution runtime + decide API + decision history (the decision event stream, PLAN.md §3.3):
- `domain.Execute` is a **pure, deterministic** DAG traversal (input → … → output) over a published
  graph. Node engines: **Input, Assignment, Rule, Split, Scorecard, Decision Table, 2D Matrix, Code,
  Output**. Conditions/expressions use **expr-lang**; the **Code** node runs **Starlark** (no
  clock/random/IO, recursion off, bounded by a step limit) with the context as a `data` dict and its
  top-level assignments merged back. Unsupported node types (AI, Connect) fail loudly.
- Each `/decide` records a stream — `DecisionStarted` → `NodeEvaluated`…  → `DecisionCompleted` /
  `DecisionFailed` — so a run is replayable node-by-node; a flow-logic error is a recorded **failed**
  decision (HTTP 200, `status: "failed"`), not a swallowed error.
- **Versioning / rollout:** `POST /v1/flows/{flow_id}/deployments` pins which version is live per
  environment (sandbox/production) and configures an optional **A/B challenger** taking
  `challenger_pct` of decisions. Decide routes accordingly and records the chosen version + variant
  (champion/challenger), so replay is stable; with no deployment it falls back to the latest version.
- HTTP: `POST /v1/flows/{slug}/{env}/decide` → `{decision_id, status, data}`;
  `GET /v1/decisions` · `GET /v1/decisions/{decision_id}` — history with the full node trace + variant.

Next in Phase 1 (see [../PLAN.md](../PLAN.md) §4.1, §8): CEL conditions (alternative engine),
analytics-lite, and the Svelte Flow builder + inline test runs.
