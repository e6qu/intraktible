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

### PROD-3 — Postgres event log: seq order can diverge from commit order `OPEN`
`platform/eventlog/postgres.go:148` `Append` inserts with `BIGSERIAL ... RETURNING
seq` in autocommit. Two concurrent appends can be assigned seq 4 and 5 and commit
in the order 5, 4. The poller (`platform/eventlog/delivery.go:126` `dispatch`)
advances `lastPub` to the max seq it read, so seq 4 — committed after 5 was
published — is **never delivered on the live bus**.

Downstream effect: `platform/projection/projection.go:222` `applyOne` sees a gap,
backfills via `Read`, and if the lower seq is still invisible,
`applyContiguous` returns an error → `Start`'s consumer goroutine calls `setErr`
and **returns permanently**. The replica's read model is then frozen and
`/healthz` reports degraded until the process is restarted.

The comment at `postgres.go:133-147` acknowledges the seq/commit inversion but
claims the runtime "re-applies the range once the lower seq is visible (on the
next poll or a restart)". **The "next poll" half of that claim is false** — the
consumer goroutine has already exited. Fix the comment along with the bug.

Chosen fix: serialize appends with a transaction-scoped advisory lock
(`BEGIN; SELECT pg_advisory_xact_lock(<const>); INSERT ... RETURNING seq; COMMIT`)
so seq assignment and commit both happen under the lock and commit order is
strictly seq order. This makes the poller's max-seq watermark correct with no
xid/visibility arithmetic, and is directly testable against the real Postgres
service already present in the `postgres` CI job (concurrent-append test
asserting commit order == seq order). Cost: globally serialized appends — a
defensible trade for an event log that is the system of record.

Keep the projection's loud gap refusal as defence in depth: it fails loudly
rather than substituting behavior, so it is a guard, not a fallback.

### PROD-1 — `--log=file` in production only warns `OPEN`
`server/server.go:630` logs "use --log=postgres or --log=nats for multi-replica
HA" and boots anyway. A single-process WAL behind a load balancer with N replicas
silently diverges. Under the no-fallbacks rule an advisory is not enough.
Fix: refuse to boot on `INTRAKTIBLE_ENV=production` with `--log=file` unless the
operator explicitly declares a single-replica deployment (an opt-in env var), so
the unsafe combination can never be reached by omission.

### PROD-2 — issue #144: `/` serves the SPA shell to anonymous browsers `OPEN`
`server/server.go:205` `root.Handle("/", web.Handler())` is unconditional; the
redirect into SSO happens only in client JS, so a server-side validator observing
`domcontentloaded` sees `200` + shell and fails the check.
Fix: fail closed server-side — an unauthenticated browser requesting a protected
route gets a redirect to `/v1/auth/oidc/<provider>/login` (or the signed-out page
that links there). Needs an explicit configured posture, because a self-hosted
deployment with no browser-session provider has nothing to redirect to: resolve
with a loud refusal on incoherent config, **not** a silent "no provider → serve
the shell as before". Assert it at the HTTP layer, not only in the browser.

### PROD-4 — no readiness drain on SIGTERM `OPEN`
`cmd/intraktible/main.go:147-164`: on signal the process goes straight to
`srv.Shutdown`. `/readyz` keeps answering 200 until the listener closes, while
Kubernetes endpoint removal is asynchronous — so a load balancer keeps routing to
a pod that has stopped listening, producing 502s on every rollout.
Fix: flip `/readyz` to 503 on signal, keep serving for a bounded drain window,
then shut down; add a matching `preStop` hook to both Helm deployments.

### BROKEN-1 — silent duration-parse fallback at shutdown `OPEN`
`cmd/intraktible/main.go:157-161`: a malformed `INTRAKTIBLE_SHUTDOWN_TIMEOUT` is
swallowed (`if err == nil`) and the default silently used — a banned fallback.
Fix: parse and validate at **startup** (not at shutdown, which is far too late to
report a config error) and refuse to boot on a bad value.

---

## Not yet swept (do these before declaring the audit complete)

- Fake/stub hunt across the four components + `platform/` (the `platform/ai`
  Stub is legitimate and opt-in via `INTRAKTIBLE_AI_STUB`; verify nothing else
  silently substitutes canned data).
- CI gap analysis: can any job silently skip or pass without asserting?
  Specifically the `postgres` job's skip-when-unset pattern.
- Remaining `web/` audit: Playwright coverage vs. real routes.
- Run the full gate locally: `make ci`, web checks, Playwright e2e,
  `make e2e-embedded`, `terraform-check`, `container-release-check`.
- `deploy/docker-compose.prod.yml` + `docs/DEPLOY.md` / `docs/DR.md` claims vs.
  what the code actually does.

---

## CONFIRMED-OK (do not re-investigate)

- `web/src/lib/api.ts:220` `normalizeFlow`'s `f.versions ?? []` — absorbs Go
  `omitempty` on a 200 OK; non-2xx throws via `errorOrStatus`. Contract
  normalization on the success path, not a fallback.
- Helm scheduler tier is a correct singleton: `replicas: 1` **and**
  `strategy: { type: Recreate }`, so a rolling update cannot transiently run two
  sweep loops. (`deploy/helm/intraktible/templates/deployment-scheduler.yaml`)
- `platform/projection/projection.go` implements genuine cross-replica
  single-writer: durable bootstrap and live apply both serialize on a
  `GetForUpdate` lock of the checkpoint row.
- The frontend is genuinely wired to the real `/v1` API; the `mock` references
  under `web/src/routes/` are comments about the wasm demo's fetch bridge.
