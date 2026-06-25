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

1. **Feature layer is read-time aggregation, not a feature store — L.** Features are
   `count`/`sum` over the event log computed at decide time. There is no precompute,
   no point-in-time correctness, no caching, no feature versioning/lineage, and a
   narrow aggregation set. A point-in-time feature store is the headline differentiator
   of commercial credit-risk platforms; this is the biggest data gap.
2. **Connector catalog is HTTP templates, not integrations — M–L.** The catalog is
   generic HTTP/GraphQL/SQL fetchers plus labelled stubs, not real bureau/KYC/fraud
   adapters with correct schemas (Experian/TransUnion/Equifax/LexisNexis/Plaid). SQL is
   SQLite-only. Even a handful of real adapters would change the evaluation.
3. **Scorecard node is additive weight-sum only — M.** No score bins/bands, no
   per-band reason codes, no scaling/calibration. Credit teams expect banded scorecards;
   this is the weakest core node and the cheapest credible deepening.
4. **No model training — L.** Models are hand-authored JSON or external endpoints.
   There is no fit/cross-validation/feature-importance pipeline. Either add light
   training or position explicitly as serve-only (the registry + drift + external-model
   story is strong on its own).
5. **Expression-language ergonomics — M.** expr-lang ships without string/date/math
   helper builtins, forcing Starlark for trivial transforms. A standard function
   library would materially improve authorability.
6. **Drift covers predictions only — M.** PSI/KL run on the prediction distribution;
   there is no feature/covariate drift and no actuals/ground-truth reconciliation to
   measure live model performance.
7. **Thin example/template library — S.** There is no in-product starter-flow gallery
   (credit STP, fraud, onboarding, KYC/AML). The flow-as-code import exists; authoring a
   few polished importable starters is low-effort, high-perceived-value.
8. **SLO attainment is all-time cumulative, not windowed — S.** A long-lived flow's
   recent breach is diluted; a rolling window is needed for an operational SLO. (Honestly
   disclosed in `analytics`.)
9. **Audit query is an O(n) log scan — M (scale).** Correct and append-only, but reads
   the whole event log per query; needs an indexed audit projection at scale. (Disclosed.)

## Where intraktible genuinely leads

To keep the ledger honest in both directions: the **single-binary self-host** (embedded
UI, SQLite/Postgres/NATS, no cloud dependency), the **event-sourced replayable core**,
and the **SR 11-7 / model-risk inventory** are real strengths that the commercial
decisioning SaaS and the orchestration engines do **not** match out of the box. These
are under-sold, not over-sold.
