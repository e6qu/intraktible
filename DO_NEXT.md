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

1. **OPEN — Await the user merge of the E6 journeys+audit PR (#166).** The E6
   vertical (#165) is merged as authoritative `origin/main` `c560b1d`. The
   continuation PR closes the remaining §8b.7 scope: live native/Wasm browser
   journeys for the modeling cockpit (3 native + 3 real-Wasm), a whole-scope
   audit that implemented dependent-aware schema/model retirement (previously
   unguarded) and narrowed the overstated streaming/bulk-ingestion claim to E7,
   and phase-closing docs (PLAN §8b.7 delivered, BUGS E6, JOURNEYS modeling).
   All local gates are green (`make ci` exit 0; 138 native, 89 real-Wasm, 254
   frontend units, race Go CI, Terraform 18/18). It is the sole open review item;
   do not start E7 before the user reports it merged.
2. **OPEN — E7 and E8 remain serialized behind E6.** Do not implement or open
   them concurrently. After the E6 continuation merges, fetch and deliberately
   reconcile authoritative `origin/main`, confirm the PR queue empty, and only
   then cut E7. Detailed boundaries and exit evidence are in `PLAN.md` §§8b.8–8b.9.
   Note: high-volume streaming/bulk ingestion and cursor pagination were
   explicitly deferred to E7's scale scope during the E6 audit.

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
