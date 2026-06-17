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
- A version may carry an `input_schema`; `decide` validates the caller's input against it before
  recording — a contract violation is a 400, not a recorded decision. The validator (shared
  `platform/schema`) covers a broad JSON-Schema subset: `type`, `required`, `properties`,
  `additionalProperties`, `enum`, `const`, the combinators `allOf`/`anyOf`/`oneOf`/`not`, numeric
  bounds (`minimum`/`maximum`/exclusive/`multipleOf`), string `minLength`/`maxLength`/`pattern`/
  `format`, array `items`/`minItems`/`maxItems`/`uniqueItems`, nested object schemas, and local
  `$ref`. Unknown keywords stay lenient.
- HTTP (under `/v1/`, X-Api-Key / session auth, org+workspace scoped):
  - `POST /v1/flows` — create `{slug, name}` → `{flow_id}`
  - `POST /v1/flows/{flow_id}/versions` — publish `{graph, input_schema}` → `{version, etag}`
  - `GET /v1/flows` · `GET /v1/flows/{flow_id}` — registry read model
  - `POST /v1/flows/{flow_id}/backtest` — replay `{version?, compare_version?, dataset}` → outcome diff
- Run it: `intraktible serve --modules=decision-engine`.

Done — execution runtime + decide API + decision history (the decision event stream, PLAN.md §3.3):
- `domain.Execute` is a **pure, deterministic** DAG traversal (input → … → output) over a published
  graph. Node engines: **Input, Assignment, Rule, Split, Scorecard, Decision Table, 2D Matrix, Code,
  ManualReview, Reason, Output** (a ManualReview node escalates to the Case Manager — opens a case;
  a **Reason** node emits structured adverse-action `{code, description}`s into the reserved
  `reason_codes` field — always surfaced by Output — which the history projector lifts to a first-class
  `reason_codes` field on the decision record for ECOA/Reg B + insurance explainability).
  Conditions/expressions use **expr-lang**; the **Code** node runs **Starlark** (no
  clock/random/IO, recursion off, bounded by a step limit) with the context as a `data` dict and its
  top-level assignments merged back. A **Connect** node calls a Context Layer connector and an **AI**
  node runs an Agent Manager agent — both pre-resolved by the shell, with the result injected under
  `connect.<output>` / `ai.<output>` (see below).
- Each `/decide` records a stream — `DecisionStarted` → `NodeEvaluated`…  → `DecisionCompleted` /
  `DecisionFailed` — so a run is replayable node-by-node; a flow-logic error is a recorded **failed**
  decision (HTTP 200, `status: "failed"`), not a swallowed error.
- **Versioning / rollout:** `POST /v1/flows/{flow_id}/deployments` pins which version is live per
  environment and configures an optional **A/B challenger** taking `challenger_pct` of decisions.
  Decide routes accordingly and records the chosen version + variant (champion/challenger), so replay
  is stable; with no deployment it falls back to the latest version.
- **Change governance (maker-checker / four-eyes):** a direct deploy is allowed only for non-production
  environments. A **production** deployment must be *proposed* by one user
  (`POST /v1/flows/{flow_id}/deployment-requests`) and *approved by a different user*
  (`POST …/deployment-requests/{req_id}/approve`, or `…/reject`) — the approval is what actually
  deploys. The proposer cannot approve their own request; every request + decision is recorded on the
  flow (an auditable approval trail). Combined with RBAC, proposing needs the `editor` role and
  approving needs `approver`.
- **Discussion threads:** deployment requests (the approve/reject/promote subject) and decisions carry a
  comment thread via the platform's `platform/comments` capability (`GET/POST /v1/comments/{type}/{id}`,
  e.g. `deployment_request` / `decision`) — an explanation trail surfaced in the builder's approvals queue
  and on the decision detail page.
- **Promotion (`POST /v1/flows/{flow_id}/promote {from,to}`):** three environments in order —
  **sandbox → staging → production** — and a promote action that ships the version currently live in
  `from` up to `to`, carrying the champion only. A non-production target deploys directly; promoting into
  **production** opens a maker-checker request (the same four-eyes gate), so the chain can't be
  short-circuited. Surfaced in the builder's Deployment panel; requires the `approver` role.
- **Backtesting (`decision-engine/backtest`, pure):** `POST /v1/flows/{flow_id}/backtest` with
  `{version?, compare_version?, dataset}` replays a dataset of inputs through a published version —
  and optionally diffs it against another version — over `domain.Execute` only. It records **no**
  decision and performs **no** I/O, so it is a safe pre-deploy confidence check; the report gives an
  exact outcome summary (completed/failed/changed counts) plus the changed records. The builder UI
  exposes it as a panel. Datasets are capped (2000 inputs; 200 returned records).
- **Analytics-lite:** a metrics projection folds the decision stream into per-flow counters
  (volume, completed/failed, average duration, and breakdowns by environment, version, and
  **variant** — so champion vs challenger outcome rates are directly comparable). `GET
  /v1/flows/{flow_id}/metrics`.
- HTTP: `POST /v1/flows/{slug}/{env}/decide` → `{decision_id, status, data}`;
  `GET /v1/decisions` · `GET /v1/decisions/{decision_id}` — history with the full node trace + variant.
  These reads honor the workspace's **PII masking** config (`platform/privacy`): fields named in
  `GET/PUT /v1/privacy` are redacted in the returned input/output/node-traces and in JSON exports, at the
  read boundary — the stored event log keeps the real values.
- **Batch decisioning:** `POST /v1/flows/{slug}/{env}/decide/batch` with `{dataset:[…], entity_type?,
  entity_id?}` runs each input through the **same recorded decide path** — every row is a real decision
  (in history, metrics, and the audit log), unlike backtest which records nothing. Returns a summary
  (completed/failed/rejected) + per-row results; a row that fails input validation/lookup is `rejected`
  (no decision), a row whose flow logic errors is a recorded `failed`. Dataset capped at 500. Surfaced as
  a builder panel.
- **Policies (`decision-engine/policy`):** the operational disposition layer over a flow — a first-class,
  versioned, governed artifact (create/publish like flows; authoring needs `editor`) that maps a flow's
  output to a **disposition** (`approve` / `decline` / `refer`) via ordered expr-lang bands + a default.
  The decide path resolves the policy bound to the flow (`ActiveForFlow`, latest version) and applies it
  to the output, recording the disposition + the policy version on the decision (replay-stable; lifted
  first-class onto the history record and returned by `decide` / `decide/batch`). It is the shared brain
  for real-time (faster/STP), bulk, and pre-approval decisioning. A policy that can't evaluate
  refers (routes to a human) rather than failing a completed decision. The completed disposition rolls up
  into analytics (`by_disposition` → an automation rate). **Disposition backtest**: `POST
  /v1/policies/{id}/backtest` `{spec?, compare_version?, flow_version?, dataset}` replays a dataset through
  the bound flow + the (draft or published) bands and reports the disposition distribution — and, vs a
  compare version, the rows that flip — recording nothing (safe tuning). API: `POST /v1/policies`,
  `POST /v1/policies/{id}/versions`, `GET /v1/policies[/{id}]`. UI: a `/policies` page authors the bands
  and previews impact.
- **Pre-approvals (`decision-engine/preapproval`):** durable, time-boxed pre-decisions for an entity —
  granted with the offer **terms** + provenance (policy/flow) + a validity window, and **honored
  instantly at decide** time: a `decide` request that names a pre-approved entity (`entity_type` +
  `entity_id`) is completed straight from the grant — the stored disposition + terms become the result
  and the flow is skipped, recorded with `preapproval_id` for provenance (the honor also increments the
  grant's honored count via its own stream event, so replay stays exact). A grant supersedes the entity's
  prior one; revoke or expiry invalidates it (expiry checked at read time, so the projection stays
  clock-free). API: `POST /v1/preapprovals` (grant, `editor`), `GET /v1/preapprovals[/{type}/{id}]`,
  `POST /v1/preapprovals/{type}/{id}/revoke`. UI: a `/preapprovals` page grants, lists (with live
  active/expired/revoked status + honored count), and revokes.
- **Promote a batch to pre-approvals** (`POST /v1/flows/{slug}/{env}/preapprove/batch`, `editor`): the
  bridge from bulk decisioning to durable pre-decisions. A population (`{dataset, entity_type,
  entity_key, disposition?, valid_days, note?}`) runs through the recorded decide path (applying the
  flow's bound policy), and every row the policy disposes to the target disposition (default `approve`)
  is granted a time-boxed pre-approval keyed by the row's `entity_key` field — its decision output
  becomes the stored offer terms. Returns a per-row tally (granted / skipped / failed / rejected). The
  builder's **Promote to pre-approvals** panel drives it over the batch dataset. This is the "policy
  informs bulk decisions" loop: decide a population once, pre-approve the winners, honor them instantly.
- **Monitors (`decision-engine/monitor`):** thresholds over a flow's live metrics — `failure_rate`,
  `refer_rate`, `automation_rate`, `approve_rate`, `decline_rate`, `avg_latency_ms`, `volume`, and
  **`distribution_drift`** — each a rule `{metric, op (gt|lt), threshold}` that **fires** when breached.
  The evaluator is a pure function of a snapshot (metrics + an optional baseline); status (`actual` /
  `computable` / `firing`) is computed at read time, never stored, so it stays correct as decisions
  accrue (a metric with no data reads "no data", not a false 0). **Distribution drift** measures the
  largest shift of any disposition share from a **captured baseline**: `POST /v1/flows/{id}/baseline`
  snapshots the current approve/decline/refer mix; `GET /v1/flows/{id}/drift` reports per-bucket
  baseline→current deltas + the max drift; a `distribution_drift` monitor alerts on it like any other.
  API: `POST /v1/flows/{id}/monitors` (`editor`), `GET /v1/flows/{id}/monitors` (rules + live status),
  `DELETE /v1/flows/{id}/monitors/{monitor_id}`, and `POST /v1/flows/{id}/monitors/check` (evaluate +
  push firing rules to webhooks). UI: a **Monitors** panel defines rules, captures a baseline, and shows
  firing/ok/no-data + the live drift readout.
- **Monitor scheduler (`monitor.Scheduler`):** an optional background sweep — set
  `INTRAKTIBLE_MONITOR_INTERVAL` (e.g. `1m`) and the server evaluates every monitor on that cadence,
  delivering to webhooks only on the **ok→firing edge** (recording an `Alerted` event) and resetting on
  `firing→ok` (a `Resolved` event), so a steadily-firing monitor is not re-sent each tick. Off by default
  (the `…/monitors/check` endpoint is the on-demand alternative). `Tick` does one tenant-wide sweep;
  `Run` wraps it on a ticker.
- **Notifications (`decision-engine/notify`):** an outbound webhook channel that makes monitors
  actionable. `POST /v1/webhooks` (`editor`) registers an http(s) endpoint; a monitor **check** POSTs the
  firing set (`{flow_id, checked_at, fired:[…]}`) to every active webhook and records each `Delivered`
  event (so deliveries show in the audit log and update the webhook's last-delivery state). Delivery
  reuses the connector **egress guard** (`connectors.EgressPolicy.Client` — SSRF-safe at dial time),
  injected as a plain `*http.Client` so `notify` stays decoupled from the context layer (main wires the
  guarded client). API: `POST|GET /v1/webhooks`, `DELETE /v1/webhooks/{id}`; UI: a webhook list + a
  "Check & notify" action in the Monitors panel. (Pull-based today — a scheduled push remains.)
- **Flow export** (`decision-engine/export`, pure): a flow version renders to **Mermaid**
  (`flowchart`, `stateDiagram-v2`), **BPMN 2.0 XML with BPMNDI** layout (opens laid-out in
  bpmn.io / Camunda; node types map to start/end events, gateways, business-rule/service/script/user
  tasks), **Graphviz DOT** (`dot -Tsvg`/`-Tpng`), and **round-trippable JSON** (`{slug,name,version,
  etag,graph,input_schema}` — the `{graph,input_schema}` subset re-imports via `POST …/versions`); a
  **decision run** renders to a Mermaid **sequenceDiagram** trace, a **Graphviz DOT** path, or the full
  **decision-record JSON**. Exposed via
  `GET /v1/flows/{flow_id}/export?format=mermaid|mermaid-state|bpmn|dot|json[&version=N]`,
  `GET /v1/decisions/{decision_id}/export?format=mermaid|dot|json`, the `intraktible export` CLI, and the
  builder + decision-detail UI (download/copy per format).
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

The builder has **structured config panels for every node type** — the flat ones (split, connect, ai,
manual_review, output, code, assignment) and the nested-table ones (rule, scorecard, decision_table,
2d_matrix, with when→then / factor / row→output repeaters and a matrix cell grid) — with the raw-JSON
textarea kept as a per-type advanced view. The canvas supports **drag-to-connect** (drag between node
handles to add an edge) alongside the from/to form (D10). It also **imports a flow JSON** (paste or
upload a JSON export, or a bare `{graph}` / `{nodes,edges}` object) onto the canvas — the inverse of the
JSON export — to review and publish; `input_schema` is preserved across edits, imports, and republishes.
(CEL as a second condition engine was closed by decision — expr-lang + Starlark already cover it.)
