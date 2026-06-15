# Context Layer

A component of **intraktible** (see [../PLAN.md](../PLAN.md) §4.3). New here? Start at [../AGENTS.md](../AGENTS.md).

Layout (functional core / imperative shell):
```
domain/      # pure types + validation + attribute merge + the feature engine (no I/O)
events/      # event payloads (Entity/Event/Feature/Connector Defined/Recorded/Fetched)
command/     # validate (pure) -> emit events
entities/    # events -> JSONB read models (entity store + per-entity event log)
features/    # events -> feature-definition read model + read-time compute (wraps domain.Compute)
connectors/  # the Connect interface + reference connectors + def/fetch read models
service/     # HTTP handlers + wiring (imperative shell)
```

Status: **done (Phase 3).**

Done — custom entities + events + feature engine + connectors (command→event→projection→API, durable & replayable):
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
- **Connectors** fetch external data. A definition is `{name, type, config}` for one of the reference
  types: **http** (calls an operator-configured REST endpoint — the "Custom Connect" case) or
  **mock_bureau** (a deterministic in-process bureau, derives a stable risk score from the params'
  `subject`). Invoking a connector is an effect performed by the shell and **recorded as a
  `ConnectorFetched` event**, so the stored response — never a re-fetch — is what replay/audit reads.
  The `Connect` interface + a registry make new connector types pluggable.
- HTTP (under `/v1/`, X-Api-Key / session auth, org+workspace scoped):
  - `POST /v1/context/entities` — record/patch `{entity_type, entity_id, attributes?}`
  - `GET /v1/context/entities?type=` — the entity list, optionally filtered by type
  - `GET /v1/context/entities/{type}/{id}` — entity detail (merged attributes + event count)
  - `GET /v1/context/entities/{type}/{id}/events` — the entity's events, newest first
  - `GET /v1/context/entities/{type}/{id}/features` — the entity's computed feature values (as of now)
  - `POST /v1/context/events` — record `{entity_type, entity_id, event_name, data?, occurred_at?}`
  - `POST /v1/context/features` — define `{name, entity_type, event_name, aggregation, field?, window_hours}`
  - `GET /v1/context/features?type=` — the feature definitions, optionally filtered by type
  - `POST /v1/context/connectors` — define `{name, type, config?}`
  - `GET /v1/context/connectors?type=` — the connector definitions, optionally filtered by type
  - `POST /v1/context/connectors/{name}/fetch` — invoke `{params?}` → `{fetch_id, response}` (recorded)
  - `GET /v1/context/connectors/{name}/fetches` — the recorded fetch history, newest first
- Run it: `intraktible serve --modules=context-layer`.

Consumed by the decision engine: a flow's **Connect node** calls a defined connector (the shell
pre-resolves it via the `connectors.Provider` adapter and injects the response under `connect.<output>`),
and Rule nodes read computed features — both through ports so the engine never imports this layer.

The HTTP connector enforces an **egress policy** (SSRF guard, D15): it dials only after DNS
resolution through a `net.Dialer` Control hook that refuses loopback / private (RFC1918, ULA) /
link-local / unspecified / multicast targets — so it guards every redirect hop and resists DNS
rebinding. Operators whose connectors legitimately reach internal hosts opt in with
`INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE` (logged at boot).

Reference connectors: **http** (Custom Connect), **sql** (a parameterized query against a
configured database — `{dsn, query, args[]}`; the pure-Go sqlite driver is built in, args bind as
named parameters so caller params cannot inject SQL, results bounded to 1000 rows), and a
deterministic **mock_bureau**.
