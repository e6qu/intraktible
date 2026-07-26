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

### GATE-1 — the shauth-sso CI job has not been run locally `OPEN`
It needs a checkout of `e6qu/shauth` at the pin in `.github/workflows/ci.yml`
(`74735a1710fa69d472e7eb27ae95ce317c7c1a3d`) plus Ory Hydra and PostgreSQL, via
`make test-shauth-sso SHAUTH_SOURCE_DIR=…`. Every other gate has been run
locally and is green. This one is being left to CI on the PR — **check it**,
because the browser gate (`platform/httpx/browsergate.go`) changes exactly the
serving path that job exercises. Highest-risk interaction: the harness may
navigate to `/` anonymously and now receive a 303 where it expected the shell.

### PERF-1 — measure Postgres append throughput under the new advisory lock `OPEN`
Appends to the Postgres event log now serialize on `pg_advisory_xact_lock`
(correct, and the only way the poller's watermark is sound), but that bounds
write throughput on the networked log and nothing measures it. `docs/PERFORMANCE.md`
already lists "throughput numbers under a durable log or Postgres" as open; this
makes it concrete. If the ceiling turns out too low, the alternative design is
recorded in the `Append` doc comment (gate the poller watermark on
`pg_snapshot_xmin` instead, leaving appends concurrent).

---

## Not yet swept (do these before declaring the audit complete)

- Remaining fake/stub hunt across the four components. Done so far: `platform/ai`
  Stub (opt-in, honest), `mock_bureau` (explicitly named, honest), drift decode,
  three swallowed unmarshals, seventeen frontend `.catch(() => [])` sites.
  Not yet swept: `agent-manager/`, `case-manager/`, `mrm/`, `registers/`,
  `reconsideration/`, `retention/` for the same patterns.
- `docs/` claim-vs-code sweep beyond DEPLOY.md/DR.md (COMPETITIVE, ENTERPRISE,
  GAPS, JOURNEYS, LAUNCH make product claims that were not checked).
- `deploy/docker-compose.yml` (non-prod) and the split profile under the new
  `--log=file` refusal — the refusal is production-only, so the split profile's
  shared-SQLite log is unaffected, but this was reasoned about, not executed.

---

## CONFIRMED-OK (do not re-investigate)

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
