<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# DO_NEXT — the live work queue

Ordered. Work top-down. When an item is done, move a one-line entry to
`WHAT_WE_DID.md` and delete it here. Keep this file short — it is the queue,
not the history.

Status markers: `OPEN` (found, not fixed) · `CONFIRMED-OK` (investigated,
genuinely fine — recorded so it is not re-investigated).

Categories: FAKE · BROKEN · PROD (multi-replica/deploy) · CI · SEC · CORRECT ·
DOC (a claim not backed by code).

---

## Queue

Enterprise roadmap execution from authoritative E2 merge `565e1d2`.

1. **OPEN — E3-1 · CORRECT: case types are ungoverned strings.**
   `domain.RequestReview.Validate` only checks non-empty `case_type`; creation
   does not pin a definition version or validate context, required evidence,
   state transitions, dispositions/reasons, priority, service calendar, or
   role-aware layout. Add a versioned event-sourced definition and pin every
   opened case to the exact version.
2. **OPEN — E3-2 · PROD: there is no configurable routing aggregate.**
   Cases have only a free-form assignee. Add durable queues, reviewer
   skill/capacity/jurisdiction/conflict profiles, ordered rules, deterministic
   routing explanations, atomic cross-replica route/claim/reassign/escalate,
   and scheduler recovery for decision-escalated cases.
3. **OPEN — E3-3 · CORRECT: evidence and related work are not modeled.**
   `CaseView` has only opaque context/notes/source decision. Add typed links to
   decisions/entities/agents/connectors/cases/alerts, immutable attachment
   metadata with hash/storage reference, required-evidence completeness,
   audited downloads, and lifecycle subject/lawful-basis/hold metadata without
   introducing a binary-storage dependency.
4. **OPEN — E3-4 · PROD: bulk work is a browser-side `Promise.allSettled`.**
   `cases/+page.svelte` independently calls assign/status per row, so the
   backend has no bounded request, idempotency, authoritative result manifest,
   or coherent partial-failure audit. Add server-owned bulk assignment,
   priority, status, and disposition operations plus search, saved views,
   duplicate detection, and queue rebalance.
5. **OPEN — E3-5 · CORRECT: no QA/second-review or feedback truth model exists.**
   Add deterministic sampling, independent second-review claims, agreement/
   override/disagreement outcomes, reasoned reviewer feedback, and explicit
   validated-outcome evidence that never treats one reviewer as ground truth.
6. **OPEN — E3-6 · DOC/UI/PROD: operational control is incomplete.**
   Add workload/capacity, first-action, resolution, ageing, SLA, routing
   failure, QA and bottleneck analytics; case-specific notifications/webhook
   attempt visibility and retry/dead-letter operations; audit export; PII-aware
   layouts and retention/hold/erasure status; API/OpenAPI/Go+TS SDK, real seed,
   and browser journeys including rebuild/restart and competing reviewers.

PRs E4–E7 remain ordered behind E3 in `PLAN.md` §§8b.5–8b.8. Do not open or
implement them concurrently; after each merge, reconcile fresh `origin/main`
before starting the next vertical tranche.

---

## CONFIRMED-OK (do not re-investigate)

- **The split profile works across processes.** Run and exercised, not reasoned
  about: an entity written on the context-layer container (:8083) appears in the
  decision-engine container's audit read (:8081) at seq 1, so the shared SQLite
  log genuinely carries events between processes. All four modules answer
  /healthz and /readyz, and the browser gate behaves on split nodes too.
- **Append throughput is measured on Linux** (docs/PERFORMANCE.md): the lock
  costs ~1.36x uncontended and ~2.12x at eight appenders, and appends still scale
  ~1.9x from one appender to eight via group commit. Do NOT re-measure on Docker
  for macOS and treat the result as a regression — that path's fsync dominates
  and produces the superseded "flat throughput" reading.

- **Every documented `/v1` endpoint exists.** All 50 `/v1` paths mentioned across
  `docs/`, `README.md` and `AGENTS.md` were diffed against the 157 routes actually
  registered in Go (both the `mux.HandleFunc("GET /v1/…")` and the table-style
  `{Method:…, Pattern:…}` registrations — miss the second and you get false
  positives). Everything resolves; the only non-matches are concrete examples with
  real names substituted for path params. No documented endpoint is a facade.
- **`docs/GAPS.md`'s "verified in code, not marketing" claims hold.** The
  expression evaluator is expr-lang v1.17.8 (a real compiled VM); Predict
  implements logistic / gbm / expression / egress-guarded external; the decision
  table has five DMN hit policies (first, unique, any, rule_order, collect) plus
  COLLECT aggregation.
- The NATS backend has **no** equivalent of the Postgres commit-order inversion:
  JetStream assigns the sequence at publish-ack and the push consumer delivers
  every message in stream order, so there is no watermark to skip past.

- The swallowed-error sweep is complete across both the Go backend packages and
  the journey-level `web/src/` pass.
- `context-layer/connectors/resilience.go` retry loop and
  `platform/secretbox` multi-key decrypt attempt are legitimate control flow —
  both fail loudly when every attempt is exhausted.

- The `shauth-sso` CI job **passes with the browser gate in place** (run
  30223404990, 4m19s). This was the highest-risk interaction in the change: that
  job drives a real Shauth + Ory Hydra + PostgreSQL stack through a browser, over
  exactly the serving path `platform/httpx/browsergate.go` now gates. It does not
  need re-running locally.

- `web/src/lib/api.ts` `normalizeFlow`'s `f.versions ?? []` — absorbs Go
  `omitempty` on a 200 OK; non-2xx throws via `errorOrStatus`. Contract
  normalization on the success path, not a fallback.
- `web/src/lib/api.ts` `res.json().catch(() => ({}))` inside `errorOrStatus` and
  friends — best-effort parse of an error body on a path that then throws. Not a
  fallback.
- Helm scheduler tier is a correct singleton: `replicas: 1` **and**
  `strategy: { type: Recreate }`, so a rolling update cannot transiently run two
  sweep loops.
- `platform/projection/projection.go` implements genuine cross-replica
  single-writer via a `GetForUpdate` lock on the checkpoint row, in both the
  durable bootstrap and the live apply.
- The frontend is genuinely wired to the real `/v1` API; `mock` references under
  `web/src/routes/` are comments about the wasm demo's fetch bridge.
- `platform/ai` Stub is opt-in via `INTRAKTIBLE_AI_STUB` and never silently
  substituted — a previous silent-fallback bug there was already fixed, and
  `server/env.go:52-56` documents it.
- No shipped production path uses `--log=file`: `docker-compose.prod.yml`, the
  Helm chart, and the ECS Terraform module all use `--log=postgres`.
