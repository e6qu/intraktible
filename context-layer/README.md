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
- **Features** are windowed signals over an entity type's event stream — a small **feature store**: a
  definition is `{name, entity_type, event_name, aggregation, field?, window_hours}` (re-defining the
  same name overwrites and bumps a monotonic `version`). The pure `domain.Compute` folds an entity's
  events — keeping those whose name matches and whose `occurred_at` falls in `(asOf-window, asOf]` —
  into one of `count | sum | avg | min | max | last | first | count_distinct` over a top-level `field`
  (a matching event missing the field contributes nothing; a present non-numeric field fails loudly).
  Because the upper bound is `asOf`, passing a past instant yields the **point-in-time** value —
  reproducible for a historical decision (`GET .../features?as_of=<RFC3339>`); the stored log stays
  clock-free. A computed value carries its **lineage** (definition version + event count). A per-entity
  **materialized read-through cache** (`context_feature_values`) serves a warm live value without
  folding the stream, invalidating on a new entity event, a redefinition, or window expiry.
  `features.Provider` adapts the engine to a `name->value` lookup for one entity; the **decision
  engine** consumes it through a port so a decide call carrying an `{entity_type, entity_id}` ref gets
  these folded into its input under `features.*` (read by Rule nodes).
- **Connectors** fetch external data. A definition is `{name, type, config}` for one of the
  types: **http**/**graphql** (operator-configured REST/GraphQL — the "Custom Connect" case — with
  an optional `auth` block (bearer | header | basic | query | oauth2) + custom headers), **sql**
  (**sqlite** or **postgres** — postgres runs read-only-transaction SELECTs with `$1` args),
  **static**, the provider adapters **plaid** (credentials injected into the request body, env-selected
  base URL), **stripe** (bearer secret key, GET retrieval), **credit_bureau** (Experian/Equifax/
  TransUnion inquiry, normalized to a common `{provider, score, band, reason_codes}` shape via
  configurable field paths), **sanctions** (deterministic in-process OFAC/EU/UN/PEP name screening
  against an operator watchlist — token-set fuzzy match, no network), or **mock_bureau** (a
  deterministic in-process reference bureau). Credential fields are sealed by the secret keyring and masked at the HTTP
  boundary; config is **validated at define time** (a bad endpoint/credential fails on save, not on
  first fetch). Invoking a connector is an effect performed by the shell and **recorded as a
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

Connector credential fields (`dsn`, `password`, `token`, `api_key`, `authorization`, etc.) are
encrypted before a `ConnectorDefined` event is recorded when
`INTRAKTIBLE_CONNECTOR_SECRET_KEY` is set to a 32-byte base64 or hex key. The event log and
projections then hold ciphertext envelopes for those fields; connector fetches decrypt just in time,
and list APIs still return redacted config. Losing the key makes encrypted connector definitions
unusable.

Reference connectors: **http** (Custom Connect), **sql** (a parameterized query against a
configured database — `{dsn, query, args[]}`; the pure-Go sqlite driver is built in, args bind as
named parameters so caller params cannot inject SQL, results bounded to 1000 rows), and a
deterministic **mock_bureau**.
