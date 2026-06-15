# Agent Manager

A component of **intraktible** (see [../PLAN.md](../PLAN.md) §4.4). New here? Start at [../AGENTS.md](../AGENTS.md).

Layout (functional core / imperative shell):
```
domain/      # pure types + validation (no I/O)
events/      # event payloads (AgentDefined, AgentRunRecorded)
command/     # validate (pure) -> emit events; running an agent invokes the AI provider
agents/      # events -> JSONB read models (agent registry + run log) + the run helper
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
- **Async runs** (event-sourced): `run {async:true}` records an `AgentRunStarted` (status `running`)
  and queues the work, returning `202 {run_id, status:"running"}` immediately; a worker pool invokes
  the provider and records the terminal `AgentRunRecorded` (poll the run for the outcome). In-flight
  runs finish on graceful shutdown, and a run left `running` by a crash is re-enqueued at boot
  (`RecoverRunning` folds the log). The synchronous path is unchanged.
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
  - `POST /v1/agents/{name}/run` — run `{prompt, async?}` → sync `{run_id, status, text?, structured?, error?}` or async `202 {run_id, status:"running"}`
  - `GET /v1/agents/{name}/run/stream` (SSE) · `GET /v1/agents/{name}/run/ws` (WebSocket) — stream a run
  - `GET /v1/agents/{name}/runs` — the agent's run log · `GET /v1/agent-runs/{run_id}` — one run
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

Consumed by the decision engine: a flow's **AI node** runs an agent (the shell pre-resolves it via the
`agents.Provider` adapter and injects the output — structured when the agent has a schema, else
`{"text": …}` — under `ai.<output>`), through an `AgentProvider` port so the engine never imports this
layer.

A schema-constrained agent's structured output is validated against its schema (a mismatch is a
recorded failed run). A real OpenAI-compatible HTTP provider exists (`ai.NewHTTP`, configured via
`INTRAKTIBLE_AI_*` env vars); the Stub is the default fallback. Runs can be **synchronous**, **async**
(queued), or **streamed** token-by-token over SSE or WebSocket.
