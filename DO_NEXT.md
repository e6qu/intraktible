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

1. **OPEN — Implement E6 as one model/context data-science vertical.** Start
   from authoritative E5 merge `593eda9` and close every `PLAN.md` §8b.7 exit
   journey: versioned source schemas and data-quality policy/incidents,
   event-time corrections/materialization/backfills, immutable point-in-time
   datasets, reproducible training and signed artifacts, richer evaluation and
   outcome/fairness semantics, complete lineage/retirement, APIs/SDKs/CLI/UI,
   durable scheduler/workers, replay, privacy, and multi-replica evidence.
   First map the existing context/features/models/outcomes/fair-lending/job
   substrate and choose extension seams that preserve historical wire/replay
   behavior. The governed source-schema/ingestion seam is now implemented;
   event identity/correction/watermarks and the first durable point-in-time
   snapshot worker are now implemented. Continue with materialization/backfill
   controls, reproducible training/signed artifacts, structured evaluation,
   model lineage/retirement, and richer outcomes/fairness are implemented.
   Scheduler retention/quality sweeps, subject-level snapshot/backfill
   crypto-shred, stable multi-replica artifact signing, and unified
   source-to-serving lineage/comparison reads, explicit independent evaluation,
   OpenAPI, the initial Go/TypeScript SDK and CLI/UI surfaces, and the real
   seeded replay plus operational-state restore are implemented. The
   public-contract slice (source-health/materialization/artifact/comparison
   reads across OpenAPI, RBAC, SDKs, CLI, UI) is complete with all local
   release gates green (`make ci` exit 0, Terraform 18/18, web gates, seed
   round trip). PR #165 is the sole open review item; its exact-head hosted
   run `30630868192` (`720e857`) is green across all nine jobs. Await the user
   merge; then continue E6 with live native/Wasm browser journeys for the
   modeling cockpit, whole-diff audits, and phase-closing documentation.
2. **OPEN — Re-audit every remaining §8b.7 scope claim against executable
   evidence.** In particular inspect quality incident lifecycle/impact,
   backfill controls, dataset exclusion/consent/export, artifact provenance and
   external registration, explanation limitations, dependent-aware retirement,
   bulk/streaming/job result contracts, CLI evidence export, and
   modeler/validator/operator/executive UI. Implement or narrow false claims;
   do not phase-close from the seeded happy path alone.
3. **OPEN — E7 and E8 remain serialized behind E6.** Do not implement or open
   them concurrently. After E6 merges, fetch and deliberately reconcile
   authoritative `origin/main`, confirm the PR queue empty, and only then cut
   E7. Detailed boundaries and exit evidence are in `PLAN.md` §§8b.8–8b.9.

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
