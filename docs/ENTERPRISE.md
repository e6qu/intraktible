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

### Identity & access  (status: authentication only)
- **P0 — RBAC.** Roles (admin / editor / viewer / approver) and authorization on
  every mutating endpoint. Today any authenticated caller can publish/deploy.
- **P1 — SSO** (SAML / OIDC) and **SCIM** user provisioning; map IdP groups → roles.
- **P1 — API token management** (scoped tokens, rotation, expiry, per-token audit).
- **P2 — Fine-grained, per-flow/per-environment permissions.**

### Governance & change control  (status: deploy + maker-checker shipped, UI included)
- **P0 — Maker-checker (four-eyes) approvals — ✅ done (backend + UI).** A production
  deploy must be *proposed* by one user and *approved* by a *different* one. The
  builder now has a **Deployment panel**: live-version-per-environment badges,
  deploy-to-sandbox, propose-for-production, and a pending-approvals queue with
  approve/reject (self-approval is refused — four-eyes), plus A/B challenger %.
- **P1 — Promotion workflow** sandbox → staging → production with gates.
- **P1 — Change history / diff** between versions (what changed, by whom, why).
- **P2 — Scheduled / time-boxed deployments**, instant rollback button.

### Auditability & compliance  (status: audit surface shipped; reason codes next)
- **P0 — Immutable audit surface — ✅ done.** `GET /v1/audit` (`platform/audit`) is a
  tenant-scoped, filterable, exportable read straight over the append-only event
  log: filter by stream / actor / event type / resource id / RFC3339 time range,
  newest-first, with a `?format=csv` export. It is admin-gated (read-only but
  sensitive) and surfaced as an **Audit log** UI page. The data was always in the
  log; this makes "who did what, when" first-class instead of operator-CLI-only.
- **P0 — Reason codes / adverse-action explainability.** Lending (ECOA/Reg B) and
  insurance require human-readable reasons for a decline. The node trace is the
  raw material; it needs structured reason-code output.
- **P1 — PII handling**: field-level classification, masking in traces/logs,
  configurable retention & purge, right-to-erasure (GDPR/CCPA).
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
- **P1 — Shadow / canary deploys** (the A/B challenger is a start).
- **P1 — Flow unit tests / assertions** stored with the flow (given input → expect
  output), run in CI and pre-deploy.
- **P2 — What-if / sensitivity analysis.**

### Observability & operations  (status: per-flow metrics + /healthz)
- **P1 — Alerting**: failure-rate, latency, volume, and outcome-distribution
  **drift** alerts (the metrics exist; alerting/thresholds do not).
- **P1 — Dashboards & SLOs**; structured request tracing (OpenTelemetry).
- **P2 — Cost tracking** for AI nodes.

### Data & integrations  (status: http / sql / mock connectors, now with a management UI)
- *A **Context data** UI (`/data`) now lists/defines connectors and features and browses
  entities + their event timelines — closing the gap where flows referenced connectors/features
  by name that could only be created via the API.*
- **P1 — A connector catalog** (credit bureaus, KYC/AML, fraud, document/OCR) with
  managed **secrets**. *Credential config fields (dsn/password/token/secret/api_key/
  authorization/…) are now redacted at the HTTP boundary (`connectors.RedactConfig`),
  so secrets never reach the client/UI — though they are still stored in plaintext
  in the projection; a real secret store / encryption-at-rest remains the P1 work.*
- **P1 — Batch decisioning** (score a file / a population) + a feature store.
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
- **P1 — Client SDKs** (the `/decide` call is the product's hot path; a typed SDK
  matters) and a **stable, versioned API** contract (OpenAPI).
- **P1 — Flow-as-code / IaC** (export+import flows, GitOps, CI promotion).

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
| 5 | **Reason codes** | P0 — adverse-action / explainability | S–M |
| 6 | **Secrets management** for connectors | P1 | M |
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

Four P0 items are implemented: **RBAC** (`platform/auth` roles +
`platform/httpx` per-request authorization), **maker-checker approvals** (the
Decision Engine refuses direct production deploys; a deployment must be *proposed*
by one user and *approved* by a different one — four-eyes — via
`/v1/flows/{id}/deployment-requests` + `…/approve`), **backtesting**
(`/v1/flows/{id}/backtest` replays a dataset through the pure engine and diffs two
versions before deploy), and the **immutable audit surface** (`GET /v1/audit`, a
filterable + CSV-exportable read over the event log). The remaining P0 is
**reason codes**; the rest are sequenced above. None requires re-architecting —
they extend the same event-sourced core.
