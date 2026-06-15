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

Status: **in progress (Phase 4).**

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
  (Only the deterministic **Stub** provider is wired today; real adapters are tracked in `BUGS.md`.)
- HTTP (under `/v1/`, X-Api-Key / session auth, org+workspace scoped):
  - `POST /v1/agents` — define `{name, provider?, model?, system?, schema?, tools?}`
  - `GET /v1/agents` · `GET /v1/agents/{name}` — the agent registry
  - `POST /v1/agents/{name}/run` — run `{prompt}` → `{run_id, status, text?, structured?, error?}`
  - `GET /v1/agents/{name}/runs` — the agent's run log · `GET /v1/agent-runs/{run_id}` — one run
- Run it: `intraktible serve --modules=agent-manager`.

Next (PLAN §4.4, to close the phase): wire the decision engine's **AI node** to run an agent during a
flow (a provider port + adapter, like features/connectors); **human-in-the-loop** escalation to the
Case Manager; richer monitoring metrics; and an agents UI.
