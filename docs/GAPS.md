<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# Honest gaps & competitive scope

This document is deliberately candid about where intraktible is **thin or missing**,
so the docs don't oversell. It complements [Enterprise readiness](./enterprise.html),
which covers the regulated-enterprise envelope. Read this one for the unflattering
truth: what a buyer comparing intraktible against commercial decisioning platforms
(credit/risk/fraud SaaS) and against process-orchestration engines (e.g. **Camunda**)
will find genuinely absent or shallow.

## What is genuinely real (so the gaps are in context)

Verified in code, not marketing: the decision engine actually executes every node
type with input-varying results; the expression evaluator is a real VM; Predict is
real math (logistic / gradient-boosted trees / expression / egress-guarded external);
everything is event-sourced and replayable; four-eyes maker-checker, RBAC, OIDC/SAML
SSO, SCIM, AES-256-GCM at-rest encryption, crypto-shred erasure, and an SR 11-7 model
inventory are all real and enforced. The decision table is DMN-grade (five hit
policies + aggregation). **None of the gaps below are facades** — they are honestly
missing or honestly shallow capabilities.

## Positioning: a decision engine with light, durable orchestration

intraktible is a **decision engine** first: a `/decide` call runs a flow as a
deterministic DAG pass and returns a recorded outcome. It now also does **durable
human-task orchestration** — a flow can pause mid-graph and resume — but it is **not** a
full process-orchestration engine (Camunda-class), and does not claim to be.

| Capability | Orchestration engines (Camunda) | intraktible |
| --- | --- | --- |
| Durable wait states / human tasks that suspend & resume | yes | **yes** — a `manual_review` node with `suspend` pauses the decision (event-sourced `DecisionSuspended`) and resumes via `POST /v1/decisions/{id}/resume`, injecting the reviewer's outcome |
| Long-running process instances | yes | **partial** — a suspended decision is a durable instance, but there is no separate process/instance model beyond the flow |
| Timer-driven resume (a paused decision auto-resumes after a delay) | yes | **no — deliberate non-goal** — a human task waits for the human; time pressure is handled by *reminders* (due-soon + overdue notifications and a webhook escalation), not by auto-advancing the decision |
| Message/signal events, correlation | yes | **no** (possible, unbuilt) — resume is reviewer-driven today |
| Parallel gateways / fork-join / multi-instance | yes | **no** — `split` is exclusive-only |
| Compensation / sagas / sub-processes | yes | **no** |

The durable suspend/resume is real and replayable (the resumed decision's trace spans
the pre- and post-pause nodes). **Timer-driven resume is a deliberate non-goal** — a
suspended human task is meant to wait for a human, and the SLA reminder/notification
system nudges the reviewer rather than auto-resolving the step. The other absent
primitives — message/signal resumption, parallel fork-join, sagas — are not present
today.

**Scale-to-zero, by design.** A suspended decision is *not* a workflow held resident in a
worker's memory (the actor/worker model of Temporal/Camunda, which keeps the instance
alive). It is just a `DecisionSuspended` event in the durable log plus its projected
read-model record — **pure data at rest**. While paused it consumes **no compute and no
resident memory**; the server can scale to zero or restart entirely, and the suspended
decision survives and rehydrates from the event log only when someone resumes it. This is
a property of the event-sourced core, not an extra subsystem — so it adds no architectural
complexity. It is proven by a cold-rebuild test (`history.TestSuspendedDecisionSurvivesColdRebuild`):
the entire read model is discarded and rebuilt from the log, and the suspended decision
still resumes to completion.

**Reminders, not auto-resume.** A human step *waits* for the human — nothing auto-resolves
it. A reminder/notification layer instead pulls reviewers to their pending tasks: the
notifications projector turns case lifecycle events into inbox items (assigned → the
assignee; due-soon and overdue → the assignee, driven by the SLA sweep), and an
*unassigned* task is addressed to a shared **reviewer queue** that every review-capable
user sees. An overdue task also escalates to the operator-configured **webhook**
(egress-guarded, the same channel as monitor alerts). This reuses the existing inbox +
SLA sweep + webhook plumbing — no new subsystem.

## Deficiencies & shallow areas (prioritized)

Sizes are rough effort to become competitively credible. **S** = days, **M** = ~1–2
weeks, **L** = a real project.

1. **Feature store — DONE (incremental precompute still open).** The feature layer is
   now a feature store: (a) a wider aggregation set — count, sum, avg, min, max, last,
   first, count_distinct; (b) **point-in-time correctness** — `Compute` windows against
   an explicit `as_of` instant (only events that had occurred by then), exposed as
   `GET .../features?as_of=<RFC3339>`, so a decision's features are reproducible; (c)
   **versioning + lineage** — every (re)definition bumps a monotonic version, and a
   computed value carries the version + the event count that fed it; (d) **precompute +
   caching** — a per-entity materialized read-through cache (`context_feature_values`)
   serves a warm value without folding the event stream, invalidating on a new entity
   event, a redefinition, or window expiry. Remaining (optional): proactive/incremental
   materialization (maintaining rolling aggregates) for very high-volume entities, where
   the read-through fold on a cold/expired value is still O(events).
2. **Connectors — real adapters added; long-tail catalog still HTTP templates.** The
   subsystem already had real Plaid/Stripe provider adapters (preconfigured host + auth,
   egress-guarded, OAuth2). Added: a **credit-bureau** adapter (Experian/Equifax/
   TransUnion — inquiry POST under the operator's auth, response normalized to a common
   `{provider, score, band, reason_codes}` via configurable field paths, so a scorecard
   reads one shape regardless of bureau); a **sanctions/PEP screening** connector (a
   deterministic in-process OFAC/EU/UN name screener — token-set fuzzy match against an
   operator watchlist, no network, replayable); and **Postgres** support in the SQL
   connector (positional `$1` args, a read-only transaction so a connector can't mutate
   the DB — verified against a real Postgres). The catalog's credit-bureau and
   watchlist templates now instantiate these real adapters instead of http stubs.
   Remaining (optional): MySQL (same driver-import pattern), and the long tail of
   labelled catalog templates (fraud/OCR/device-risk) that are still generic http
   scaffolds — a real adapter per vendor is added on demand.
3. **Scorecard node — banded, DONE (calibration still open).** The scorecard now
   supports score bands: the summed score falls into the highest band whose `min` it
   reaches, which labels the outcome (a grade, written to a configurable `band` output)
   and emits that band's adverse-action reason codes (the standard `{code, description}`
   shape the history projector lifts). Bands are authored in the builder's node
   inspector and validated at publish. Remaining (optional): scaling/calibration
   (points-to-double-odds), which few evaluations require.
4. **Model training — DONE (logistic).** `POST /v1/models/train` fits a
   logistic-regression model from a labelled dataset and defines it — a servable
   `KindLogistic` model, no hand-authored coefficients. The fit is a deterministic batch
   gradient descent (z-scored features, de-standardized back to raw space) with k-fold
   **cross-validation** (AUC / log-loss / accuracy) and **feature importance**
   (standardized-coefficient magnitude). Being deterministic, re-training the same
   dataset/options reproduces the model and report. Authored from the `/models` builder.
   Remaining (optional): GBM/tree training (the serve path already supports `gbm`), and
   a dataset registry so a training set is stored/versioned rather than posted per run.
5. **Expression-language ergonomics — DONE.** expr-lang in fact ships a full standard
   library (strings/numbers/collections/date-parse); the gap was that it was
   undocumented, so authors reached for Starlark unnecessarily. It is now cataloged in
   `docs/EXPRESSIONS.md` (v2). In the same pass the one non-deterministic builtin,
   `now()`, was disabled and is rejected at publish — closing a latent replayability
   hole in the "no clock, no I/O" guarantee.
6. **Model monitoring — DONE (covariate drift + actuals).** Alongside the existing
   prediction-distribution PSI, the drift report now includes **covariate (input-feature)
   drift**: a prediction records the model's input feature values, the projector folds
   per-feature running mean/variance (Welford), and `GET /v1/models/{name}/drift` reports
   each feature's standardized mean shift + variance ratio vs the captured baseline — a
   leading indicator that precedes prediction drift. **Actuals reconciliation**:
   `POST /v1/models/{name}/outcomes {probability, label}` records realized outcomes,
   bucketed by predicted decile, and `GET /v1/models/{name}/performance` reports
   calibration, accuracy, Brier score, and realized AUC — live performance from ground
   truth. Remaining (optional): full per-feature PSI histograms (mean/variance shift is
   the bounded signal today) and decision-linked outcome recording (caller supplies the
   probability today).
7. **Example/template library — DONE.** A "New from template" gallery on the Flows page
   ships 6 importable starters (Consumer Credit STP, CNP Fraud, Sanctions/PEP, KYB, BNPL,
   Chargeback) that exercise the differentiating node types. Remaining (optional): convert
   a *seed* flow to scorecard/decision_table so those nodes appear in seeded, not just
   template, flows.
8. **SLO attainment — rolling window, DONE.** The metrics projection now retains a
   bounded ring of per-UTC-day outcome buckets (90 days, pruned relative to the newest
   day so replay stays deterministic), and an SLO may set a `window_days` (0 = all-time).
   Attainment over the window keeps a long-lived flow's recent breach from being diluted
   by its lifetime history; the SLO card shows the window and lets an operator set it.
9. **Indexed audit projection — DONE.** The audit read no longer folds the whole
   (multi-tenant) event log per query. A platform-level `audit.Projector` re-indexes
   every event into an `audit_entries` collection keyed by `(org, workspace, seq)`, so a
   query is an INDEXED prefix range scan of just the caller's tenant (the store's
   C-collation `(collection, key)` index bounds it on SQLite/Postgres) over pre-derived
   compact entries — not a re-read of every event. Trade-off: the audit read is now
   eventually-consistent (a projection) like every other read model, and the index
   duplicates the payload for the resource-value filter; both are the standard cost of
   an indexed read model and it rebuilds from the log on replay.

## Where intraktible genuinely leads

To keep the ledger honest in both directions: the **single-binary self-host** (embedded
UI, SQLite/Postgres/NATS, no cloud dependency), the **event-sourced replayable core**,
and the **SR 11-7 / model-risk inventory** are real strengths that the commercial
decisioning SaaS and the orchestration engines do **not** match out of the box. These
are under-sold, not over-sold.
