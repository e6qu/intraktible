# Agent Manager

A component of **intraktible** (see [../PLAN.md](../PLAN.md) §4.4). New here? Start at [../AGENTS.md](../AGENTS.md).

Layout (functional core / imperative shell):
```
domain/      # pure types + validation (no I/O)
events/      # event payloads (AgentDefined, AgentRunRecorded)
command/     # validate (pure) -> emit events; running an agent invokes the AI provider
agents/      # events -> JSONB read models (agent registry + run log) + the run helper
governance/  # governed templates/releases/evals/deployments/assists/incidents
service/     # HTTP handlers + wiring (imperative shell)
```

Status: **done (Phase 4).**

Done — agent definitions + runs (command→event→projection→API, durable & replayable):
- An **agent** is a configuration over the pluggable AI provider (`platform/ai`): a `name`, an
  optional `provider` + `model` selection, a `system` prompt, an optional structured-output JSON
  `schema`, and a declared `tools` set. `AgentDefined` registers it; re-defining the same name
  overwrites.
- **Running** an agent invokes the provider with that config and the caller's `prompt`; the response
  (text, or schema-constrained structured output) is captured in an `AgentRunRecorded` event. The
  model call is the only effect — recording the response makes a run auditable and means **replay
  reads the recorded output** rather than re-calling the (non-deterministic) model. A provider failure
  is a recorded `failed` run, not an API error. The run log doubles as the monitoring projection.
- **Tool-calling**: when an agent declares `tools` and a `Toolbox` is wired, running it drives a
  bounded tool-calling loop — the model may answer with tool calls, each is executed via the Toolbox
  and fed back, until it returns a final answer (or the step limit trips, a recorded `failed` run).
  Every tool call (name, arguments, result/error) is recorded on the run, so a tool-using run is fully
  auditable and replay-stable. The reference `tools.ConnectorToolbox` exposes Context Layer connectors
  as tools. The OpenAI-compatible HTTP provider supports tool-calling; the **Stub** answers directly.
- **Async runs** (event-sourced): `run {async:true}` durably accepts one
  idempotent logical run before returning `202 {run_id, status:"running"}`. A
  scalable worker pool claims each attempt with an owner, lease, and heartbeat,
  then records completed, failed, cancelled, timed-out, or dead-letter state.
  Explicit retry increments the bounded attempt count; a conflicting
  `Idempotency-Key` reuse is refused. A worker lost mid-attempt is recoverable
  after lease expiry without every API replica independently re-enqueuing it.
  The synchronous and streaming paths remain available.
- **Streaming runs** (token-by-token, configurable transport): the provider boundary gained a
  `StreamingProvider` (Stub chunks word-by-word; the HTTP provider parses OpenAI SSE deltas), and
  `StreamRun` streams deltas while still recording the terminal run. Two transports: **SSE**
  (`GET /v1/agents/{name}/run/stream?prompt=`) and **WebSocket** (`GET /v1/agents/{name}/run/ws`,
  send a `{prompt}` message → `{type:"chunk"}`… `{type:"done"}`). The builder's agent page lets you
  pick the transport. A tool-using or non-streaming agent runs normally and emits its text as one
  chunk, so the interface is uniform.
- HTTP (under `/v1/`, X-Api-Key / session auth, org+workspace scoped):
  - `POST /v1/agents` — define `{name, provider?, model?, system?, schema?, tools?}`
  - `GET /v1/agents` · `GET /v1/agents/{name}` — the agent registry
  - `POST /v1/agents/{name}/run` — run `{prompt, async?, version?,
    timeout_ms?, max_attempts?, business_reference?, correlation_id?}`; async
    requests may use `Idempotency-Key`
  - `GET /v1/agents/{name}/run/stream` (SSE) · `GET /v1/agents/{name}/run/ws` (WebSocket) — stream a run
  - `GET /v1/agents/{name}/runs` — the agent's run log · `GET
    /v1/agent-runs/{run_id}` — one run · `POST
    /v1/agent-runs/{run_id}/cancel` — request cancellation · `POST
    /v1/agent-runs/{run_id}/retry` — explicitly retry a failed terminal run
  - `POST /v1/agents/{name}/runs/{run_id}/escalate` — open a case from a run → `{case_id}`
  - `GET /v1/agent-runs` — all runs · `GET /v1/agent-runs/summary` — run monitoring roll-up
- **Human-in-the-loop**: escalating a run opens a **Case Manager** case. Because the Agent Manager is
  built *after* the Case Manager, it emits the Case Manager's own `ReviewRequested` event (which the
  `cases` projector already consumes) with the run referenced in the case context — so the dependency
  direction stays one-way (this module imports case-manager, never the reverse) and no `cases` change
  is needed.
- **Monitoring**: `GET /v1/agent-runs/summary` rolls up the run log (totals, completed/failed, by agent).
- **UI** (`web/src/routes/agents`): the registry (list/define agents + a run-summary banner) and a
  per-agent view that runs the agent, shows the run log, and escalates a run to a case.
- Run it: `intraktible serve --modules=agent-manager` (UI dev: `make dev`).

Governed production operations live alongside, rather than replacing, that low-level
registry. `agent-manager/governance` provides:

- reusable templates and immutable releases with typed contracts, dependency pins,
  citations/evidence rules, trust labels, tool modes, budgets, and timeouts;
- immutable representative/adversarial suites, repeated deterministic and governed
  semantic campaigns, human adjudication, release gates, paired comparison, and
  reproducible exports;
- assigned four-eyes release review plus scheduled environment deployment, pause,
  rollback, approval expiry, retirement, safety incidents, circuit containment, and
  explicit guarded recovery;
- subject-sealed evidence-cited case assists with durable replica-safe workers,
  cancellation, bounded retry/dead letter, human-before-call tool continuation,
  stale-evidence UX, accountable reviewer actions, value-free differences, and
  QA/outcome/cost analytics; and
- a versioned remote-agent protocol that preserves scoped inputs, invocation
  identity, cancellation, typed evidence, and platform-owned tool authorization.

The governed UI starts at `/agents/governance`; the reviewer experience is embedded
in `/cases/{case_id}` rather than creating a second work queue. All lifecycle
contracts are also exposed through OpenAPI, the Go and TypeScript SDKs, and
`intraktible agents`.

Consumed by the decision engine: when traversal reaches a flow's **AI node**,
the shell invokes the `agents.Provider` adapter with the exact current record
and records the effect before resuming with structured output (or
`{"text": …}`) under `ai.<output>`. The `AgentProvider` port keeps the engine
from importing this layer. The node can name an immutable agent `version`: `0` follows latest for sandbox iteration, while
staging and production decisions and previews require a positive version and invoke exactly that
historical config. Updating an agent therefore cannot change governed flow behavior until an author
publishes and deploys a reviewed flow version that pins the new agent version.

A schema-constrained agent's structured output is validated against its schema (a mismatch is a
recorded failed run). A real OpenAI-compatible HTTP provider exists (`ai.NewHTTP`, configured via
`INTRAKTIBLE_AI_*` env vars); with no provider, runs fail loudly. The deterministic Stub is
opt-in for development/tests only (`INTRAKTIBLE_AI_STUB=1`). Runs can be **synchronous**, **async**
(queued), or **streamed** token-by-token over SSE or WebSocket.
