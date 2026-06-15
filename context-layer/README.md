# Context Layer

A component of **intraktible** (see [../PLAN.md](../PLAN.md) §4.3). New here? Start at [../AGENTS.md](../AGENTS.md).

Layout (functional core / imperative shell):
```
domain/      # pure types + validation + attribute merge (no I/O)
events/      # event payloads (EntityRecorded, EventRecorded)
command/     # validate (pure) -> emit events
entities/    # events -> JSONB read models (entity store + per-entity event log)
service/     # HTTP handlers + wiring (imperative shell)
```

Status: **in progress (Phase 3).**

Done — custom entities + events (command→event→projection→API, durable & replayable):
- **Entities** are dynamic-JSONB records keyed by `(entity_type, entity_id)`. Recording the same
  entity again **patches** it: top-level attribute keys merge (latest wins, others retained) via the
  pure `domain.MergeAttributes`. Non-object attributes are rejected loudly.
- **Events** are custom business events about an entity (`event_name` + dynamic `data`). Recording one
  appends to the entity's per-entity event log and bumps the entity's `event_count`; an event about a
  not-yet-recorded entity auto-creates a shell entity. `occurred_at` is optional — the command fills
  it with the record time when omitted and records it in the event (replay-stable). These are the raw
  signals the **feature engine** (next slice) will aggregate into windowed counts/sums.
- HTTP (under `/v1/`, X-Api-Key / session auth, org+workspace scoped):
  - `POST /v1/context/entities` — record/patch `{entity_type, entity_id, attributes?}`
  - `GET /v1/context/entities?type=` — the entity list, optionally filtered by type
  - `GET /v1/context/entities/{type}/{id}` — entity detail (merged attributes + event count)
  - `GET /v1/context/entities/{type}/{id}/events` — the entity's events, newest first
  - `POST /v1/context/events` — record `{entity_type, entity_id, event_name, data?, occurred_at?}`
- Run it: `intraktible serve --modules=context-layer`.

Next (PLAN §4.3): the **feature engine** (windowed counts/sums over the event stream, consumed by Rule
nodes); then **connectors** (a `Connect` interface + reference connectors + the Custom Connect Node).
