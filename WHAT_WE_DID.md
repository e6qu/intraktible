<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# WHAT_WE_DID — completed work, newest last

Append one line per completed item, with the evidence that proves it done
(a passing gate, a test name, a file:line). This is the history; the live queue
is `DO_NEXT.md`.

---

## Session 1 — 2026-07-27 — production-readiness audit

Branch `hardening/production-readiness-audit`, off `292d882`.

### Setup — commit `466e817`
- Created the continuity convention (`STATUS.md`, `WHAT_WE_DID.md`,
  `DO_NEXT.md`, plus the existing `PLAN.md` as plan-of-record) and documented it
  in `AGENTS.md`.
- `CLAUDE.md` is now a symlink to `AGENTS.md`, so both entry points are one file.
- Fixed the repo-local git identity: the configured email was malformed
  (`adi11235 at gmail.com`). Now `2966430+e6qu@users.noreply.github.com` with
  `core.sshCommand` pinned to `~/.ssh/id_ed25519_e6qu`, per AGENTS.md.

### Audit findings established
- Backend is real, not scaffolding: `server/server.go` genuinely wires command
  handlers → event log → projections → HTTP for all four components.
- Frontend genuinely talks to the real `/v1` API via `web/src/lib/api.ts`; no
  mock data path in the served app.
- `go build ./...` and `go test ./...` pass at the base commit; CI on `main`
  green (run 29862257834).

### Production-path fixes — commit `ae985ed`
- **PROD-3, event loss under concurrent appends.** `PostgresLog.Append` now
  serializes on `pg_advisory_xact_lock` held to COMMIT, so commit order equals
  seq order and the delivery poller's watermark can no longer skip a
  late-committing seq. Evidence: `TestPostgresLogConcurrentAppendsPreserveCommitOrder`
  against a real PostgreSQL 16 — **verified it fails without the fix**
  (3/3 runs, e.g. 45/50 delivered with seqs 27/30/37/40/46 missing) and passes
  with it (3/3). Full `postgres` CI job green locally under `-race`.
- **PROD-2 / issue #144, launch origin did not fail closed.** New
  `platform/httpx/browsergate.go`: an unauthenticated browser *navigation* is
  redirected to the deployment's sign-in entry point instead of receiving the
  SPA shell. Entry point is derived at the composition root from the configured
  providers (signed-out page when SSO is configured, `/login` otherwise) — an
  explicit posture, not a silent fallback. Non-navigation requests and public
  paths pass through, so assets and the sign-in page still load. Evidence:
  7 tests in `browsergate_test.go`, an embedded-artifact Playwright assertion in
  `web/e2e-embedded/smoke.spec.ts`, and a live curl against the built binary
  (`GET /` → 303 `/login`; `/login`, `/healthz` → 200).
- **PROD-1, `--log=file` in production only warned.** Now a boot refusal unless
  `INTRAKTIBLE_SINGLE_REPLICA=1` explicitly declares a single-replica
  deployment. Evidence: `TestPreflightFileLogNeedsSingleReplicaDeclaration`.
- **PROD-4, no readiness drain on SIGTERM.** `/readyz` now reports 503 as soon
  as shutdown begins while the replica keeps serving, so the load balancer
  depools it before the listener closes. Evidence: `TestReadyFailsWhileDraining`.
- **BROKEN-1, silent duration-parse fallback.** A malformed
  `INTRAKTIBLE_SHUTDOWN_TIMEOUT` was swallowed in favour of the default; both it
  and the new `INTRAKTIBLE_DRAIN_DELAY` are now parsed and validated at boot
  (`envDuration`), refusing to start on a bad value.
- Corrected two comments that documented behavior the code did not have: the
  claim that a projection gap recovers "on the next poll" (the consumer
  goroutine has already exited), and "LISTEN/NOTIFY is a future optimization"
  (it is implemented).

### Second fix pass — commits `648e17c`, `977d62f`, `ecb01c7`
- **MRM under-reporting.** Every governance read in `mrm/` swallowed its error —
  assertions, shadow divergence, analytics, monitor snapshot/rules, model drift,
  and the fair-lending screen — so a flow whose evidence could not be read
  rendered as a flow with nothing to report, in the document whose purpose is to
  assert governance was checked. All now propagate. Evidence:
  `TestBuildFailsWhenGovernanceEvidenceCannotBeRead` (with a healthy-build
  control), **verified to fail without the fix**.
- **SSRF blocked-range list** silently skipped a range that failed to parse; the
  entries are compile-time constants, so it panics at init instead.
- **Malformed SAML MetadataURL** was swallowed, leaving the SP advertising its
  ACS URL as its metadata location. Now refused, like every other URL there.
- **Undecodable flow input schema** made the per-flow OpenAPI document fall back
  to a permissive "any object" — a contract integrators point codegen at, so the
  fallback produced clients that compile and then fail. Now refused.

### Sweep completed
`agent-manager/`, `case-manager/`, `registers/`, `reconsideration/`,
`retention/` swept for the same swallowed-error patterns: **clean**. The
remaining `silently`/`best-effort` matches in those trees are comments
documenting fallbacks that were already removed.

### CI evidence
- `shauth-sso` **passes with the browser gate in place** (run 30223404990,
  4m19s) — the highest-risk interaction in this branch.
- `postgres` passes with the whole-suite change (3m1s).
- `e2e-embedded`, the new job, passes (1m3s).

## Session 1 (cont.) — PR #146 and after

### Third fix pass — branch `audit/linux-throughput-and-split-profile`
- **The split profile could not be authenticated at all.** Its services run
  `--store=sqlite`, and a durable store deliberately refuses to seed the
  well-known dev key, but nothing replaced it — every call to :8081–:8084
  returned 401 with no documented way in. Found by *running* the profile rather
  than reasoning about it. The four services now set
  `INTRAKTIBLE_BOOTSTRAP_API_KEY`. Verified across processes: an entity written
  on the context-layer container appears in the decision-engine container's audit
  read at seq 1.
- **Corrected my own published throughput claim.** The append-lock numbers in
  PR #146 were taken on Docker for macOS, whose VM fsync dominated the signal and
  led me to write that throughput is "flat in the number of appenders". Re-measured
  on Linux: the lock costs ~1.36x uncontended and ~2.12x at eight appenders, and
  appends still scale ~1.9x from one to eight because group commit batches the
  flushes queued behind the lock. Both PERFORMANCE.md and ENTERPRISE.md corrected,
  with the superseded figures and their cause left on the page.
- **README config table** was missing `INTRAKTIBLE_BOOTSTRAP_API_KEY`,
  `INTRAKTIBLE_SINGLE_REPLICA`, `INTRAKTIBLE_DRAIN_DELAY` and
  `INTRAKTIBLE_SHUTDOWN_TIMEOUT` — the middle two govern whether a production node
  boots at all. Added.

### Docs-claim verification (PR #146)
- All 50 documented `/v1` endpoints diffed against the 157 routes actually
  registered: everything resolves, no facades.
- `docs/GAPS.md`'s "verified in code, not marketing" claims independently
  confirmed (expr-lang VM, four Predict kinds, five DMN hit policies).
- `docs/ENTERPRISE.md`'s networked-log claim corrected.

## Session 1 (cont.) — security review

### Reviewed and found sound (recorded so it is not re-done)
- **Tenant isolation.** Store keys are `org/workspace/`-prefixed and
  `identity.Valid` rejects `/` in either segment, so one tenant's prefix cannot
  match another's. An SSO login takes Org/Workspace from the *provider's
  configuration*, never from a token claim, so a crafted claim cannot select
  another tenant; a provider configured without them is refused at startup.
- **PII masking has no bypass.** The decision list, single read, and export all
  apply the tenant's masking config, and the config read fails closed. The
  compliance registers carry subject identifiers and compliance metadata rather
  than flow input data, so field masking does not apply to them by construction.
- **All 180 registered routes checked for mis-scoping.** None found.

### Fixed — authorization by inheritance
`requiredRole` matches path substrings behind two blanket defaults (every GET is
viewer, every other unmatched method is operator), so a new endpoint gets an
authorization level without anyone choosing one. `authz_routes_internal_test.go`
now pins the role for every registered route and scans the repo for registrations
in both mounting styles, failing on an unpinned route or a silent role move.
Verified by breaking it both ways.

## Session 1 (cont.) — remaining coverage, and what it found

### Fixed
- **The model-risk page rendered blank** for any workspace with no models — every
  new install. `/v1/mrm/report` encodes an empty inventory as `"models": null`,
  the page read `report.models.length`, and the TypeError aborted the render of
  everything below the lede with no error shown. Found by writing the page's
  first browser spec. Normalized in `getMrmReport`; every other list endpoint was
  probed on an empty workspace and none returns a null array.
- **A malformed date bound on the decisions list was silently dropped**, returning
  the unfiltered set to a caller who believed they had a time window. Now a 400,
  matching the audit surface which already refused these.

### Covered
- `?variant=` (champion/challenger "compare arms") had no test at any layer.
  Now driven with a pinned A/B roll, asserting each arm's exact share AND that
  every returned row carries the requested variant — a count-only assertion
  passes against a filter that ignores the parameter when arms are equal.
- The four specless pages now have browser specs: `/compliance`, `/fairlending`,
  `/mrm`, `/me`.

All new tests were verified by breaking the code they cover.

## Session 1 (cont.) — the networked log could not author anything

### CRITICAL — `--log=postgres` returned 400 on every flow creation
The optimistic-concurrency claims this repo mints use a **NUL separator**, and
Postgres rejects NUL in a TEXT column. Every append carrying a claim failed, so
the backend documented for multi-replica HA — selected by the Helm chart, the
ECS module and `docker-compose.prod.yml` — could not create a flow or publish a
version. Verified end to end against a real Postgres-backed server before
(`400 invalid byte sequence ... 0x00`) and after (`201`, duplicates still
conflict, distinct slugs still accepted).

Fixed by encoding the claim at the **Postgres boundary only** with
`strconv.Quote`: injective, so uniqueness semantics are identical. Not applied to
SQLite, which stores NUL correctly and has real rows in the raw form that
re-encoding would orphan.

**Why nothing caught it:** the Postgres log tests appended envelopes carrying no
claim, and every decision-engine test builds its own in-memory or WAL log — so
although CI ran the whole suite against a live PostgreSQL, the two never met.
`decision-engine/postgres_log_e2e_test.go` now runs the real authoring commands
over a real Postgres log. Both new tests verified to fail against the unfixed
encoding.

### Durable decide throughput, measured (Linux)
| backend | decisions/sec | vs in-memory |
|---|---|---|
| memory | ~16,700 | — |
| file WAL | ~9,460 | ~1.8x |
| SQLite | ~4,100 | ~4x |
| **Postgres (HA)** | **~104** | **~160x** |

Do NOT benchmark the file WAL on Docker for macOS: ~34/sec there vs ~9,460 on
Linux, entirely that VM's fsync path.

## Session 1 (cont.) — event-log claim parity

- **Backend contract matrix.** `Envelope.Unique` now runs one shared
  NUL-separated-key contract against memory, WAL, SQLite, NATS, Postgres, and
  the production encryption wrapper. Evidence:
  `TestClaimContract{Memory,WAL,SQLite,NATS,Postgres,Encrypted}`; the Postgres
  case passed against an ephemeral real PostgreSQL 15 instance.
- **NATS fail-loud startup.** A pre-existing JetStream with a too-short duplicate
  window no longer opens after a failed attempt to widen it, because doing so
  leaves optimistic-concurrency claims unenforced. Evidence:
  `TestNATSLogRefusesUnenforceableClaimConfig`, verified with a real in-process
  JetStream account denied stream-update permission.
- **Gates.** `make check` (vet, build, full `go test -race ./...`) and
  `make lint` pass. The complete `platform/eventlog` race suite also passes with
  PostgreSQL enabled. All nine PR jobs passed in CI run 30282326320: Go,
  PostgreSQL, web, browser e2e, embedded-artifact e2e, WebAssembly demo,
  Shauth/Ory SSO, Terraform, and container-release.

## Session 2 — NATS whole-log claims and stream durability

- **Permanent claims.** NATS no longer forgets `Envelope.Unique` after the
  `Msg-Id` window. A claimed event is published on an injectively encoded
  per-claim subject with `ExpectLastSequencePerSubject(0)`, making the event and
  its permanent claim one atomic write. The bounded `Msg-Id` cache remains only
  for mixed-version races. Evidence: `TestNATSUniqueClaimSurvivesDedupWindow`,
  verified to fail before the fix and pass across close/reopen after expiry.
- **Safe migration and delivery.** Opening an old base-subject stream loads its
  retained claims once and subscribes from the exact next sequence; base and
  claim subjects use one ordered multi-filter consumer. Evidence:
  `TestNATSUniqueClaimMigratesLegacyStream`, `TestNATSLog` (cross-node delivery
  of both subjects), and the shared `TestClaimContractNATS`.
- **Durable stream reconciliation.** A still-intact existing stream is made
  unbounded and protected from per-message delete/purge before use. Memory
  storage and `FirstSeq > 1` (a discarded prefix that replay cannot recover)
  refuse startup. Evidence: `TestNATSLogUnboundsIntactExistingStream`,
  `TestNATSLogRefusesMemoryStream`, and `TestNATSLogRefusesDiscardedHistory`.
- **Gates.** The complete event-log package passes under `-race`; `make check`
  and `make lint` pass repository-wide.
