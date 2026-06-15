# Context Layer

A component of **intraktible** (see [../PLAN.md](../PLAN.md) §4.3). New here? Start at [../AGENTS.md](../AGENTS.md).

Layout (functional core / imperative shell):
```
domain/      # pure types + validation + attribute merge + the feature engine (no I/O)
events/      # event payloads (EntityRecorded, EventRecorded, FeatureDefined)
command/     # validate (pure) -> emit events
entities/    # events -> JSONB read models (entity store + per-entity event log)
features/    # events -> feature-definition read model + read-time compute (wraps domain.Compute)
service/     # HTTP handlers + wiring (imperative shell)
```

Status: **in progress (Phase 3).**

Done — custom entities + events + feature engine (command→event→projection→API, durable & replayable):
- **Entities** are dynamic-JSONB records keyed by `(entity_type, entity_id)`. Recording the same
  entity again **patches** it: top-level attribute keys merge (latest wins, others retained) via the
  pure `domain.MergeAttributes`. Non-object attributes are rejected loudly.
- **Events** are custom business events about an entity (`event_name` + dynamic `data`). Recording one
  appends to the entity's per-entity event log and bumps the entity's `event_count`; an event about a
  not-yet-recorded entity auto-creates a shell entity. `occurred_at` is optional — the command fills
  it with the record time when omitted and records it in the event (replay-stable).
- **Features** are windowed signals over an entity type's event stream: a definition is
  `{name, entity_type, event_name, aggregation(count|sum), field?, window_hours}` (re-defining the
  same name overwrites). The pure `domain.Compute` folds an entity's events — keeping those whose name
  matches and whose `occurred_at` falls in `(now-window, now]` — into a `count` or a `sum` of a
  numeric top-level `field` (a matching event missing the field contributes nothing; a present
  non-numeric field fails loudly). Computation is **read-time** (windowed against now), so the stored
  log stays clock-free. `features.Provider` adapts the engine to a `name->value` lookup for one
  entity; the **decision engine** consumes it through a port so a decide call carrying an
  `{entity_type, entity_id}` ref gets these folded into its input under `features.*` (read by Rule
  nodes).
- HTTP (under `/v1/`, X-Api-Key / session auth, org+workspace scoped):
  - `POST /v1/context/entities` — record/patch `{entity_type, entity_id, attributes?}`
  - `GET /v1/context/entities?type=` — the entity list, optionally filtered by type
  - `GET /v1/context/entities/{type}/{id}` — entity detail (merged attributes + event count)
  - `GET /v1/context/entities/{type}/{id}/events` — the entity's events, newest first
  - `GET /v1/context/entities/{type}/{id}/features` — the entity's computed feature values (as of now)
  - `POST /v1/context/events` — record `{entity_type, entity_id, event_name, data?, occurred_at?}`
  - `POST /v1/context/features` — define `{name, entity_type, event_name, aggregation, field?, window_hours}`
  - `GET /v1/context/features?type=` — the feature definitions, optionally filtered by type
- Run it: `intraktible serve --modules=context-layer`.

Next (PLAN §4.3): **connectors** — a `Connect` interface + reference connectors (HTTP/REST, SQL, a
mock bureau) + a Custom Connect Node, with connector results recorded as events.
