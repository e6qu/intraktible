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

Status: **done (Phase 1).** (Phase 3 wired Context Layer features in — see below.)

Done — flow model + versioning (vertical slice, command→event→projection→API, durable & replayable):
- Flow = versioned DAG of typed nodes/edges; each `FlowVersionPublished` is immutable, numbered
  monotonically (1, 2, …) and stamped with a content `etag` over `(graph, input_schema)`.
- `ValidateGraph` fails loudly: unique node IDs of known types, exactly one Input + ≥1 Output,
  edges reference existing distinct nodes, acyclic (Kahn).
- A version may carry an `input_schema`; `decide` validates the caller's input against it (a supported
  JSON-Schema subset: object `type`, `required`, per-property `type`) before recording — a contract
  violation is a 400, not a recorded decision.
- HTTP (under `/v1/`, X-Api-Key / session auth, org+workspace scoped):
  - `POST /v1/flows` — create `{slug, name}` → `{flow_id}`
  - `POST /v1/flows/{flow_id}/versions` — publish `{graph, input_schema}` → `{version, etag}`
  - `GET /v1/flows` · `GET /v1/flows/{flow_id}` — registry read model
- Run it: `intraktible serve --modules=decision-engine`.

Done — execution runtime + decide API + decision history (the decision event stream, PLAN.md §3.3):
- `domain.Execute` is a **pure, deterministic** DAG traversal (input → … → output) over a published
  graph. Node engines: **Input, Assignment, Rule, Split, Scorecard, Decision Table, 2D Matrix, Code,
  ManualReview, Output** (a ManualReview node escalates to the Case Manager — opens a case).
  Conditions/expressions use **expr-lang**; the **Code** node runs **Starlark** (no
  clock/random/IO, recursion off, bounded by a step limit) with the context as a `data` dict and its
  top-level assignments merged back. A **Connect** node calls a Context Layer connector and an **AI**
  node runs an Agent Manager agent — both pre-resolved by the shell, with the result injected under
  `connect.<output>` / `ai.<output>` (see below).
- Each `/decide` records a stream — `DecisionStarted` → `NodeEvaluated`…  → `DecisionCompleted` /
  `DecisionFailed` — so a run is replayable node-by-node; a flow-logic error is a recorded **failed**
  decision (HTTP 200, `status: "failed"`), not a swallowed error.
- **Versioning / rollout:** `POST /v1/flows/{flow_id}/deployments` pins which version is live per
  environment (sandbox/production) and configures an optional **A/B challenger** taking
  `challenger_pct` of decisions. Decide routes accordingly and records the chosen version + variant
  (champion/challenger), so replay is stable; with no deployment it falls back to the latest version.
- **Analytics-lite:** a metrics projection folds the decision stream into per-flow counters
  (volume, completed/failed, average duration, and breakdowns by environment, version, and
  **variant** — so champion vs challenger outcome rates are directly comparable). `GET
  /v1/flows/{flow_id}/metrics`.
- HTTP: `POST /v1/flows/{slug}/{env}/decide` → `{decision_id, status, data}`;
  `GET /v1/decisions` · `GET /v1/decisions/{decision_id}` — history with the full node trace + variant.
- **Context + agents (Phase 3/4):** a decide call may carry `{entity_type, entity_id}`; the shell folds
  that entity's computed features into the input under `features.*` (so a Rule/Split expression can
  read `features.txn_count_24h`). A flow's **Connect** nodes are likewise pre-resolved (the shell
  invokes each named connector with the current input and injects the response under `connect.<output>`)
  and its **AI** nodes run an Agent Manager agent (the node's literal prompt, or the current input,
  injected under `ai.<output>`). All are recorded in `DecisionStarted` for replay stability, and the
  pure core performs no I/O. The engine reaches the (later-built) Context Layer / Agent Manager only
  through `FeatureProvider` / `ConnectorProvider` / `AgentProvider` **ports** in `command/`, satisfied
  by `features.Provider` / `connectors.Provider` / `agents.Provider` adapters wired at the composition
  root — so the dependency direction stays one-way. `WithFeatures` / `WithConnectors` / `WithAgents`
  enable them; without a provider, a flow using those nodes fails loudly.

The builder has **structured config panels** for the flat-config node types (split, connect, ai,
manual_review, output); other types still use a raw-JSON textarea. Deferred (see
[../BUGS.md](../BUGS.md)): builder drag-to-connect + structured panels for the complex node types (D10).
(CEL as a second condition engine was closed by decision — expr-lang + Starlark already cover it.)
