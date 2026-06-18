<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# Enterprise readiness — gap analysis & roadmap

intraktible is a working **decision-as-a-service** platform: model a decision as a
versioned graph of typed nodes, deploy it per environment, call `/decide`, and get
a recorded, replayable, explainable outcome — plus a Case Manager for
human-in-the-loop review, a Context Layer for features/connectors, and an Agent
Manager for AI steps. This document assesses what it would take for that to be a
*complete* product for regulated enterprises — banks, insurers, lenders — i.e. the
class of buyer served by commercial decision platforms.

It is written from the **user's** perspective (a risk/credit/ops team and the
platform team that supports them), and it is deliberately honest about what is
present, what is missing, and what matters most.

## 1. What is already enterprise-grade

These are real strengths, not placeholders:

- **Event-sourced core** — every command is an immutable event; projections are
  rebuildable; replay and as-of rollback work. This is the foundation auditability
  and reproducibility are built on, and most products bolt it on later.
- **Deterministic, recorded decisions** — effects (clock, randomness, connector/AI
  responses) are captured in events, so a decision replays to the identical result.
- **Full decision lineage** — every `/decide` records a node-by-node trace
  (input → … → output) retrievable by id and exportable (Mermaid/BPMN).
- **Versioning + environments + A/B** — immutable, etag'd versions; per-environment
  deployment; champion/challenger split with per-variant metrics.
- **Durable, pluggable storage** — SQLite/Postgres projection stores + a shared
  SQLite event log, with incremental resume.
- **Multi-tenancy** — every event and projection is org/workspace scoped.
- **Operational tooling** — `serve | log | replay | export`, health/degraded
  surfacing, crash-safe WAL.

## 2. Gaps, by category (what "complete" requires)

Priorities: **P0** = blocks a regulated production rollout; **P1** = expected by
enterprise buyers; **P2** = differentiators / scale.

### Identity & access  (status: RBAC + managed tokens shipped)
- **P0 — RBAC — ✅ done.** Roles (viewer / operator / editor / approver / admin)
  and authorization on mutating endpoints.
- **P1 — SSO** (SAML / OIDC) and **SCIM** user provisioning; map IdP groups → roles.
- **P1 — API token management — ✅ done (backend + UI).** Admin-gated
  `GET/POST/DELETE /v1/api-keys` (+ `POST …/{id}/rotate`) manages durable, hashed API
  tokens for the current org/workspace; create returns the generated secret once, tokens
  carry scope, role, actor, optional expiry, and revoke time. **Rotation** mints a fresh
  secret while honoring the prior one through a grace window (zero-downtime roll-out;
  `grace_seconds`, default immediate). Managed from an **API tokens** panel on the Audit
  (admin) page — create, rotate, and revoke, each revealing the one-time secret. Token
  create/rotate/revoke append `auth.managed_key.*` events to the log, so each token's
  lifecycle shows in the immutable audit trail (per-token deep link from the panel),
  attributed to the admin who acted.
- **P2 — Fine-grained, per-flow/per-environment permissions.**

### Governance & change control  (status: deploy + maker-checker shipped, UI included)
- **P0 — Maker-checker (four-eyes) approvals — ✅ done (backend + UI).** A production
  deploy must be *proposed* by one user and *approved* by a *different* one. The
  builder now has a **Deployment panel**: live-version-per-environment badges,
  deploy-to-sandbox, propose-for-production, and a requests queue (pending +
  decided) with approve/reject (self-approval is refused — four-eyes), plus A/B
  challenger %. Approve and reject both capture an **explanation** recorded on the
  request, and each request carries a comment thread, so the who/why is durable.
- **P1 — Promotion workflow — ✅ done.** Three environments (sandbox → staging →
  production) and a **promote** action (`POST /v1/flows/{id}/promote {from,to}`)
  that ships the live version of one env up the chain — deploying directly into a
  non-production target and opening a maker-checker request into production (the
  same four-eyes gate), and a **promotion gate** refuses to promote a flow whose
  monitors are firing or whose **assertions fail** on the target version (409 +
  details; `force` overrides when the stage allows it). A per-stage **promotion
  policy** (`GET/PUT /v1/flows/{id}/promotion-policy`) now controls, for each
  target environment, whether assertions, monitors, review, and force override
  are required/allowed; production review remains mandatory. Surfaced in the
  builder's Deployment panel.
- **Comment threads / explanations — ✅ done.** A general commenting capability
  (`platform/comments`): a durable, chronological discussion attached to any subject
  (`GET/POST /v1/comments/{type}/{id}`), surfaced on the items that get approved /
  rejected / promoted (deployment requests), on flows, policies, and decisions —
  every reviewable thing carries an audit-grade explanation trail (one reusable
  `CommentThread.svelte` drops onto any subject), with one level of **threaded
  replies** and **@-mentions** that feed a per-user **notifications inbox**
  (`platform/notifications` — a projector folds comment mentions into a recipient
  inbox; a header bell shows the unread count and marks read).
- **P1 — Change history / diff** between versions — *the builder now has a client-side
  version-diff panel (added/removed/changed nodes + edges between any two published
  versions); a richer who/why audit of changes is still open.*
- **P2 — Scheduled / time-boxed deployments**, instant rollback button.

### Auditability & compliance  (status: audit surface shipped; reason codes next)
- **P0 — Immutable audit surface — ✅ done.** `GET /v1/audit` (`platform/audit`) is a
  tenant-scoped, filterable, exportable read straight over the append-only event
  log: filter by stream / actor / event type / resource id / RFC3339 time range,
  newest-first, with a `?format=csv` export. It is admin-gated (read-only but
  sensitive) and surfaced as an **Audit log** UI page. The data was always in the
  log; this makes "who did what, when" first-class instead of operator-CLI-only.
- **P0 — Reason codes / adverse-action explainability — ✅ done.** A **Reason node**
  emits a structured `{code, description}` for every condition that holds; the codes
  accumulate in a reserved `reason_codes` field that the Output node always surfaces
  (never dropped by field selection), and the decision-history projector lifts them
  into a first-class `reason_codes` field on the decision record. The decision-detail
  UI shows a **Reason codes** section. This is the ECOA/Reg B + insurance decline-
  reason requirement.
- **P1 — PII handling**: field-level classification, masking in traces/logs,
  configurable retention & purge, right-to-erasure (GDPR/CCPA). *Field-level
  **masking** now ships (`platform/privacy`): a per-workspace sensitive-field list
  (admin-gated) whose values are redacted in decision input/output, node traces,
  and exports at the read boundary — the raw event log stays intact. Remaining:
  configurable retention/purge and right-to-erasure (which the event-sourced model
  makes non-trivial — likely crypto-shredding per subject).*
- **P1 — Model risk management (SR 11-7 / SS1/23)**: documented model inventory,
  validation evidence, monitoring — supported by metrics + versioning but not
  packaged.
- **P2 — Data residency / region pinning.**

### Testing, validation & safety  (status: backtesting + a test-run in the builder)
- **P0 — Backtesting / replay-on-dataset — ✅ done.** `POST /v1/flows/{id}/backtest`
  replays a dataset of inputs through a published version (and optionally diffs it
  against another version) using the pure engine — no decision is recorded and no
  I/O is performed. The builder exposes it as a panel that flags the records whose
  outcome changed. The deterministic engine makes this a natural, safe pre-deploy
  confidence check.
- **P1 — Shadow / canary deploys — ✅ shadow done.** A per-environment **shadow
  version** (`PUT /v1/flows/{id}/shadow {environment, version}`, 0 clears) is
  evaluated over the same input as every live decision in that environment; its
  result is never returned to the caller. A `shadow.Projector` folds the
  comparison into a per-env report (`GET …/shadow`) — total / matched / diverged /
  errored with sample diverging decision ids — answering "how often would
  promoting this candidate change the outcome?" at zero risk. Surfaced as a
  **Shadow deploys** panel in the builder. The A/B challenger already covers canary
  (a challenger takes a traffic share with its result returned). *Remaining: shadow
  re-resolves connector/AI nodes against the live input only (not its own).*
- **P1 — Flow unit tests / assertions — ✅ done.** Input→expected cases stored
  with the flow (`decision-engine/assertions`), run through the pure core via
  `POST /v1/flows/{id}/assertions/run`, and enforced as a **pre-promote gate**
  (a promote is blocked when assertions fail on the target version). UI: an
  Assertions panel in the builder. *Remaining: run them automatically in CI / a
  pre-deploy hook (today they gate promote and run on demand).*
- **P2 — What-if / sensitivity analysis.**

### Observability & operations  (status: metrics + monitors + drift + scheduled webhook alerts + /healthz)
- **P1 — Alerting — ✅ done (failure-rate, latency, volume, distribution drift).**
  Threshold **monitors** (`decision-engine/monitor`) over failure/refer/automation/
  approve/decline rate, avg latency, volume, and **distribution drift** (max shift
  of a disposition share vs a captured baseline) evaluate live; **webhook delivery**
  (`decision-engine/notify`, SSRF-safe egress, each delivery recorded for audit)
  pushes the firing set out; and a **scheduler** (`monitor.Scheduler`,
  `INTRAKTIBLE_MONITOR_INTERVAL`) sweeps on a timer, notifying only on the
  ok→firing edge (and resetting on resolve). The on-demand `…/monitors/check`
  endpoint remains for cron/manual triggers. *Remaining polish: alert routing/
  templating per channel and richer drift (PSI/KL vs simple share-delta).*
- **P1 — Dashboards & SLOs**; structured request tracing (OpenTelemetry).
- **P2 — Cost tracking** for AI nodes.

### Data & integrations  (status: http / sql / mock connectors, now with a management UI)
- *A **Context data** UI (`/data`) now lists/defines connectors and features and browses
  entities + their event timelines — closing the gap where flows referenced connectors/features
  by name that could only be created via the API.*
- **P1 — A connector catalog** (credit bureaus, KYC/AML, fraud, document/OCR) with
  managed **secrets**. *The **catalog ships** (`connectors.Catalog`, `GET
  /v1/context/connectors/catalog`): curated templates (REST / credit bureau / KYC-AML
  / fraud / document-OCR / SQL) that scaffold the connector config, surfaced as
  "start from a template" chips on the Data page. Credential config fields
  (dsn/password/token/…) are redacted at the HTTP boundary (`connectors.RedactConfig`),
  so secrets never reach the client/UI. Connector credential fields are now also
  encrypted before `ConnectorDefined` is recorded when operators set
  `INTRAKTIBLE_CONNECTOR_SECRET_KEY` (32-byte base64/hex key), so the event log and
  projections hold ciphertext envelopes while fetches decrypt just in time. **Key
  rotation** is supported via a keyring: the primary key seals new values and each
  sealed envelope records (a fingerprint of) the key that sealed it, so prior keys
  listed in `INTRAKTIBLE_CONNECTOR_SECRET_KEYS_PREVIOUS` keep already-sealed values
  readable while new writes move to the new key — rotate with no downtime, no
  re-encryption pass. Remaining polish: external KMS (the key still lives in env, not
  a managed vault) and per-template auth fields.*
- **P1 — Batch decisioning** (score a file / a population) — **DONE.** `POST
  /v1/flows/{slug}/{env}/decide/batch` runs a dataset through the recorded decide
  path (each row a real decision in history/metrics/audit; capped at 500), with a
  summary + per-row results and a builder panel. *A feature store remains.*
- **P2 — Streaming ingestion** for real-time features.

### AI / ML governance  (status: provider + tool-calling + structured output)
- **P1 — Model/prompt registry & versioning**, offline eval harness, guardrails
  (PII, jailbreak), cost/rate limits. Critical now that AI nodes can drive outcomes.

### Reliability & scale  (status: monolith + sqlite-shared-log split profile)
- **P1 — A networked log backend** (Postgres/Badger/NATS/Kafka) for true multi-node
  HA — the `Log` interface is ready; the backend is polling-based today.
- **P1 — Backups / DR runbook**, point-in-time recovery (replay already enables it).
- **P2 — Horizontal scale & multi-region.**

### Developer & platform experience  (status: REST + CLI + embedded UI)
- **P1 — Stable, versioned API contract — ✅ first pass done.** The binary serves
  its own **OpenAPI 3.1** document at `GET /openapi.json` (unauthenticated, so
  codegen/Swagger-UI/Postman can fetch it) plus a dependency-free reference page at
  `GET /docs`. The document (`platform/openapi`, embedded) covers the public
  data-plane surface — decide + batch-decide, decision history reads, flow
  list/create/read, flow-as-code import, and `/v1/me` — with an `X-Api-Key` security
  scheme. A typed **Go client SDK** (`client`) wraps that surface over net/http with
  no third-party deps — `client.New(baseURL, apiKey).Decide(…)` and friends, with
  errors surfaced as a typed `*APIError`; it is tested end-to-end against a live
  engine. *Remaining: a TypeScript/other-language SDK, and widening the spec to the
  admin/management endpoints.*
- **P1 — Flow-as-code / IaC — ✅ first pass done.** Flows export as a JSON document
  (`GET …/export?format=json`) and **import** back via `POST /v1/flows/import`: the flow
  is created when its slug is new, otherwise the graph is published as a new version.
  Import folds the authoritative log (not the read projection), so back-to-back/CI runs
  are safe, and re-importing identical content is a no-op (`published:false`, 200) —
  idempotent GitOps. Surfaced as an **Import flow (as code)** panel (paste or upload JSON)
  on the engine list. **Bundle import** (`POST /v1/flows/import-bundle`,
  `{flows:[…]}`) imports a whole repo of flows in one request, best-effort with a
  per-flow result (a bad flow is reported, not fatal); the same panel accepts a
  bundle. *Remaining: a CLI/GitOps action wrapping the endpoints.*

### Security  (status: API key + SameSite session, gosec-clean, SSRF guard)
- **P0 — Encryption at rest** for the durable stores + **secrets management**.
- **P1 — Pen testing, dependency/CVE scanning** (govulncheck is wired), **SOC 2 /
  ISO 27001** evidence.

## 3. Prioritized roadmap

| # | Item | Why | Effort |
|---|------|-----|--------|
| 1 | **RBAC** (roles + authZ) — ✅ done | P0 — nothing else is safe without it | M |
| 2 | **Maker-checker approvals** — ✅ done | P0 — change control on decision logic | M |
| 3 | **Backtesting on a dataset** — ✅ done | P0 — the user's #1 confidence tool | M |
| 4 | **Audit API + UI** — ✅ done | P0 — surface the lineage we already record | S |
| 5 | **Reason codes** — ✅ done | P0 — adverse-action / explainability | S–M |
| 6 | **Connector credential encryption + key rotation** — ✅ done; external KMS remains | P1 | M |
| 7 | **Alerting / drift** | P1 | M |
| 8 | **SSO/SCIM, batch decisioning, SDKs, networked log** | P1 | L each |
| 9 | **SOC2/ISO, data residency, multi-region** | P2 / org-level | XL |

## 4. Honest bottom line

The **engine and its event-sourced spine are production-quality** and, in some
respects (replayability, determinism, embedded self-hosting), ahead of typical
commercial offerings. What separates it today from an enterprise product is not the
decisioning core — it is the **governance, access-control, testing, and compliance
envelope** around it. Those are well-scoped, mostly tractable on the existing
architecture (events + ports), and are what this roadmap front-loads.

All five P0 items are implemented: **RBAC** (`platform/auth` roles +
`platform/httpx` per-request authorization), **maker-checker approvals** (the
Decision Engine refuses direct production deploys; a deployment must be *proposed*
by one user and *approved* by a different one — four-eyes — via
`/v1/flows/{id}/deployment-requests` + `…/approve`), **backtesting**
(`/v1/flows/{id}/backtest` replays a dataset through the pure engine and diffs two
versions before deploy), the **immutable audit surface** (`GET /v1/audit`, a
filterable + CSV-exportable read over the event log), and **reason codes** (a
Reason node emits structured adverse-action `{code, description}`s, lifted to a
first-class field on the decision record and shown in the decision UI). The
remaining work is all P1/P2 (secrets management, alerting, SSO/SCIM, SDKs,
SOC2 …); none requires re-architecting — they extend the same event-sourced core.

Beyond the P0 envelope, a **decision-automation** layer now sits over the engine:
**policies** (`decision-engine/policy`) attach auto-approve / decline / refer bands
to a flow and assign a disposition on every decision (real-time STP, with a
record-nothing disposition backtest for safe tuning); **batch decisioning** scores a
whole population through the recorded decide path; and **pre-approvals**
(`decision-engine/preapproval`) are durable, time-boxed grants that the decide path
honors instantly — a pre-approved entity is completed straight from the grant's terms,
skipping the flow, recorded with `preapproval_id` for provenance. The three join up via
**promote-to-pre-approvals** (`…/{env}/preapprove/batch`): decide a population through the
policy and turn every approved row into a durable grant keyed by a row field, so a bulk
run pre-approves the winners and they are honored instantly thereafter. One disposition
brain serves real-time, bulk, and pre-approval paths.
