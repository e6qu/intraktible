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
  `platform/httpx/browsergate.go`: an unauthenticated browser _navigation_ is
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
  returned 401 with no documented way in. Found by _running_ the profile rather
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
  match another's. An SSO login takes Org/Workspace from the _provider's
  configuration_, never from a token claim, so a crafted claim cannot select
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

| backend           | decisions/sec | vs in-memory |
| ----------------- | ------------- | ------------ |
| memory            | ~16,700       | —            |
| file WAL          | ~9,460        | ~1.8x        |
| SQLite            | ~4,100        | ~4x          |
| **Postgres (HA)** | **~104**      | **~160x**    |

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

## Session 3 — whole-product journey audit

- **Statutory retention now governs every erasure path.** Individual erasure,
  the admin-triggered bulk sweep, and the background policy scheduler all use
  the same `RetentionGate`; sweeps report manual holds and statutory holds
  separately instead of silently skipping or destroying them. Evidence:
  `TestBulkRetentionHonorsStatutoryGate`,
  `TestSchedulerPreservesHeldAndStatutorilyRetainedSubjects`, and focused
  `go test ./platform/erasure ./server`.
- **Future production deployment uses the same four-eyes control as immediate
  production deployment.** The direct schedule command now rejects production;
  a maker records the deployment window on a request and a different checker
  creates the pending schedule with the approval event. The builder routes that
  journey into its approval table and displays the window. Evidence:
  `TestScheduledProductionDeploymentIsGovernedAndAtomic`,
  `TestScheduledProductionDeploymentOverHTTP`, and zero-warning `svelte-check`.
- **Scheduled activation and expiry are atomic event-sourced transitions.**
  The enriched activation/revert events are folded by both the schedule and
  flow projections; legacy marker events remain no-ops there and replay through
  their historical companion events. No split append can strand lifecycle and
  live deployment state. Evidence:
  `TestScheduledProductionDeploymentIsGovernedAndAtomic` asserts exactly one
  event per transition, and focused decision-engine/command/flows/schedule tests
  pass.
- **Overdue-case webhook delivery is durable and replayable.** Each breached
  case remains `pending` until its own external escalation is accepted, finds
  no matching channel, or fails permanently; retryable transport/408/429/5xx
  outcomes stay pending across restart and successful cases are not sent again.
  The queue/detail UI exposes the real state. Evidence:
  `TestRetryableBreachDeliverySurvivesRestartWithoutDuplicatingSuccess`,
  `TestTickDeliversBreachWebhook`, focused `go test ./case-manager/... ./server`,
  and zero-warning `svelte-check`.
- **Case status projection now fails loudly on corrupt wire values.**
  `cases.applyStatus` no longer logs and skips an unknown status, which could
  leave a closed case looking open and eligible for SLA processing.
- **Agent-run escalation is durable and globally idempotent.** A tenant-global
  run claim permits exactly one review case across concurrent handlers; retries
  return that case, the agent path must match, and only completed runs qualify.
  The Case Manager event projects the case link back onto `RunView`, so the
  agent UI renders “Open case” after reload/replay without browser-only state.
  Evidence: `TestEscalateRunOpensCase`,
  `TestEscalateRunIsGloballyIdempotentAcrossHandlers`, agent service e2e retry /
  link / path checks, and zero-warning `svelte-check`.
- **Async-agent admission is genuinely bounded.** Queue capacity is reserved
  before `run_started` is recorded; the 257th waiting run fails loudly instead
  of launching an unbounded overflow goroutine, while boot recovery uses the
  same capacity accounting. Evidence: `TestStartRunRejectsFullQueue` and the
  complete `go test ./agent-manager/...` suite.
- **Agent projections fail loudly on corrupt statuses and invalid config JSON.**
  Unknown run statuses are no longer coerced to `failed`, and etag generation
  no longer returns an empty hash when JSON marshaling fails.
- **The in-app bell now carries real operational and approval alerts.** Flow
  monitor fire/recovery, model drift, production deployment requests and
  approvals, rollbacks, and scheduled activation/reversion project into
  role-gated shared queues with flow/model links. On-demand monitor checks now
  record the same alert/recovery transitions as scheduled checks. Evidence:
  `TestOperationalAndApprovalAlertsReachRoleQueues`, focused notifications /
  monitor / decision service / server tests, and zero-warning `svelte-check`.
- **Shared notification read state is per user.** Reviewer/operator/approver
  queue items are personalized when listed and use replayable read receipts, so
  one person marking a shared task read neither fails the ownership check nor
  hides it from colleagues. Evidence: the extended
  `TestTaskNotificationsFromCaseLifecycle` proves Bob-read/Carol-unread replay.
- **Notification and monitor projections fail loudly on impossible ordering.**
  Unknown case assignments/SLA events, unknown read targets, and alert
  transitions for unknown monitors no longer become empty/no-op state.
- **Data governance is now an executable admin journey.** The compliance UI
  saves scheduler policy, runs an explicitly confirmed retention sweep, reports
  held/statutorily-retained outcomes, and releases holds; entity detail supports
  reasoned hold, guarded release, and acknowledged crypto-shred while showing
  statutory blockers and reloading protected values out of browser memory.
  Forbidden sharing reads render unavailable rather than “sharing permitted.”
  Evidence: 74 API-client tests, zero-warning `svelte-check`, and 5 focused
  Playwright tests against the real Go backend including
  hold→release→erasure→`[erased]`.
- **Background work now participates in operational health.** Each named
  monitor, drift, deployment, case-SLA, and retention scheduler latches a failed
  tick independently and clears only after its own successful tick. Both
  `/healthz` and `/readyz` use the combined projection + live-log + scheduler
  check (fixing the prior readiness omission for event-log failure), while
  retryable webhook outcomes remain healthy successful ticks. Evidence:
  `TestSchedulerFailureDegradesHealthAndReadinessUntilRecovery` and focused
  scheduler/server package tests.
- **Model live-performance actuals are now a complete journey.** Editors can
  record predicted probability, realized binary outcome, and optional decision
  lineage from the drift panel and immediately see calibration-derived metrics.
  Baseline/monitor/outcome commands now reject nonexistent models, a first
  valid actual creates the stats record instead of disappearing, and corrupt
  alert ordering fails the projector. Evidence:
  `TestModelMonitoringCommandsRejectUnknownModelAndPreserveFirstActual`, the
  complete `go test ./decision-engine/...`, and all 3 Models Playwright tests.
- **Model maker-checker UX now matches its backend contract.** An author or
  requester sees self-approval disabled with the four-eyes reason; a different
  approver must enter a real approval/rejection rationale, which is sent into
  the immutable event instead of canned text. Evidence: the Models browser
  suite now passes 4 tests, including two real actors and asserted request
  reasoning.
- **Decision regulatory state no longer fails open.** Adverse-action issuance,
  contest, and reconsideration now load as one explicit state; any failed read
  shows the backend error and blocks “not yet issued,” contest, and human-review
  controls until Retry succeeds instead of resetting everything to null.
  Evidence: zero-warning `svelte-check` and the focused real-server Playwright
  failure journey.
- 2026-07-27: Made deployment-request replay fail before mutation on unknown or
  already-terminal approvals/rejections, and made the decide hot path trust its
  owned slug index instead of hiding misses behind a full collection scan;
  `go test ./decision-engine/flows ./decision-engine` passes with ordering,
  immutability, clean-miss, and dangling-index regressions.
- 2026-07-27: Closed the remaining journey-level read fallbacks: flow
  analytics/drift/shadow/grants and Admin privacy/token failures now render
  explicit retryable errors, permission denials stay distinct, and dependent
  mutations remain disabled until successful load; zero-warning
  `svelte-check` and all 45 Engine + Audit real-server Playwright tests pass.
- 2026-07-27: Made pre-approval revoke/honor and webhook unsubscribe/delivery
  lifecycle transitions reject unknown state instead of disappearing; command
  paths now reject unknown/double terminal actions before append, while
  unsubscribed webhook tombstones safely accept an already-in-flight delivery
  without remaining an active target. Focused preapproval, notify, command, and
  service suites pass.
- 2026-07-27: Made explicit runtime configuration fail fast: malformed
  booleans, login token-bucket values, model-drift windows, and decision
  evaluation timeouts no longer coerce to defaults; `ALLOW_PLAINTEXT=false` no
  longer bypasses production encryption preflight. Server tests pass and the
  root/Agent Manager docs now describe the real opt-in Stub and validated knobs.
- 2026-07-27: Removed the browser clipboard legacy fallback and closed two
  builder misrepresentation/data-loss paths: invalid test JSON no longer
  produces a copyable `{}` request, invalid node JSON renders as invalid and
  cannot be overwritten through structured controls, and trace polling retries
  only projection-lag 404s while surfacing other failures. `svelte-check` is
  clean, 210 Vitest tests pass, and both focused browser regressions pass.
- 2026-07-27: Stopped Observability from turning an SLO read failure into “No
  objective set,” and made failed per-model drift summaries render an
  unavailable badge with the backend error instead of disappearing. The
  zero-warning Svelte check and all 8 Observability + Models browser tests pass.
- 2026-07-27: Made the connector catalog authoritative in Context Data:
  creation is disabled with an inline retryable error until it loads, the type
  picker is derived from the catalog, and the previously omitted explicit
  `mock_bureau` development template now makes the catalog cover every supported
  connector type. Connector unit tests and all 3 Context Data browser tests pass.
- 2026-07-27: Made scheduled deployment cancellation a claimed, state-aware,
  atomic transition: pending cancel races activation safely; active cancel races
  expiry safely and restores (or removes) the captured baseline; a newer
  deployment is never overwritten; impossible replay ordering now fails loudly.
  Focused Decision Engine command/projection/service suites pass, including the
  active, no-prior, superseded, and duplicate-terminal regressions.
- 2026-07-27: Completed the real two-actor scheduled-production browser journey:
  the editor proposes a future window, the shared approval notification
  deep-links the independent checker to the deploy tab, the recorded approval
  creates and immediately displays a pending schedule without deploying early,
  and scheduled approval no longer claims the version is already live.
  Zero-warning `svelte-check` and the focused real-server Playwright journey pass.
- 2026-07-27: Unified the durable human-task terminal journey: suspension records
  its case id before projection, generic case completion is refused while the
  source decision waits, `DecisionResumed` completes the exact linked case during
  replay, and every personal/shared notification for the resolved task is
  tombstoned out of live inboxes. The case UI now directs reviewers to record an
  approve/decline/refer outcome on the decision. Focused Decision Engine, Case
  Manager, notification, zero-warning Svelte, and real-browser case→decision→case
  regressions pass.
- 2026-07-27: Turned model approval into a real governance handoff: requests enter
  the role-gated shared approval inbox and deep-link to the named Governance
  panel; the Manager cockpit/nav now counts and lists both flow and model
  approvals instead of misrouting to entity pre-approvals; terminal
  approve/reject events retire exact requests from every approver's inbox (also
  fixing stale flow approvals). Notification tests, 211 Vitest tests,
  zero-warning Svelte check, and the real two-actor model browser journey pass.
- 2026-07-27: Reconciled the executable journey/launch guides with the real
  retention, scheduler, durable-task, model-governance, and scheduled-deployment
  contracts; the builder now states that Test runs the persisted
  published/deployed graph, warns while canvas edits are dirty, disables execution
  before first publish or a selected non-sandbox deployment, and never emits a
  copyable request for invalid JSON. Preview now enforces the same
  staging/production deployment boundary as a recorded run. The complete Decision
  Engine suite, zero-warning Svelte check, and focused authoring browser journeys
  pass.
- 2026-07-27: Closed the remaining documented discussion handoffs: `policy`
  mention notifications now deep-link to the exact selected policy and anchored
  discussion rather than becoming inert, and model mentions open the exact model
  discussion rather than an undifferentiated registry (while model approvals
  retain their exact Governance destination). Real different-author browser
  journeys, the model-approval handoff, all Policy + Notifications Playwright
  tests, zero-warning Svelte check, and all 211 Vitest tests pass.
- 2026-07-27: Closed the whole-project validation loop: 119 native browser
  journeys, 80 real-wasm journeys, 3 embedded-binary smokes, the production web
  build, zero-warning TypeScript format/lint/type checks, and the complete
  race-enabled Go check pass. Copy-paste detection exposed two new
  lifecycle/scheduler clones; schedule reconstruction now shares one checked
  initializer and flow/model monitors share a tested health/metrics timed-loop
  shell. `dupl` reports zero groups; gosec, deadcode, and licenses pass.
  `govulncheck` then identified GO-2026-6061 as the sole open dependency gate,
  recorded in `DO_NEXT.md` pending explicit update approval.
- 2026-07-27: Cleared the final security gate with the approved Google-owned,
  Apache-2.0 gRPC v1.81.1→v1.82.1 update. The module diff changes no other
  version; `govulncheck` reports zero reachable vulnerabilities, licenses pass,
  and the complete race-enabled Go suite remains green.
- 2026-07-27: Reconciled the completed audit against a fresh authoritative
  `origin/main` (still exactly `47d024b`, no open PR), verified every new source
  file carries the AGPL SPDX header and generated/embed trees are clean, and
  passed the final post-update `make ci` release gate.
- 2026-07-27: Committed the single 120-file whole-product audit as `f54c80b`,
  pushed `hardening/production-readiness-audit`, and opened PR #155 as the
  repository's sole review queue.
- 2026-07-27: Babysat PR #155 through run 30300119151: all nine jobs pass,
  including Go race/security/license gates, web, native e2e, real-wasm demo,
  embedded artifact, real PostgreSQL, Shauth SSO, Terraform, and container
  release.
- 2026-07-27: PR #155 merged as `fea7a26`; refreshed the audit branch from that
  authoritative commit, confirmed the remote PR queue is empty, and began the
  next whole-product journey pass.
- 2026-07-27: Completed the Context data browser journey: editors can create or
  update entities, record entity events, observe feature recomputation, invoke a
  connector, and inspect its durable fetch evidence without exposing sealed
  configuration. Commands wait for their exact event sequence to reach the read
  model before reloading; focused Go/API checks and all 5 Context Data browser
  journeys pass.
- 2026-07-27: Corrected lawful-basis lifecycle semantics end to end: consent list
  responses expose clock-evaluated `active`, the entity UI records future expiry
  and distinguishes active/expired/withdrawn, and Compliance excludes expired
  records from active/basis/expiring counts. HTTP, unit, and real-browser expiry
  regressions pass.
- 2026-07-27: Closed the regulated-decision handoff: adverse-action issuance,
  contest, and reconsideration clients retain the durable event acknowledgement
  and wait for projection application; Compliance lists actionable open contests;
  the journey catalogue now covers fair-lending, notice issuance, contests,
  reconsideration, lawful-basis expiry, and sharing. The real decline → notice →
  contest → overturned review → resolved queue browser journey passes.
- 2026-07-27: Established one browser-wide read-after-write contract for the
  event-sourced backend: every successful mutation carrying `seq` waits for
  `/readyz.applied` before its caller can reload, on both native and Wasm
  transports. Recorded decide/resume (including manual-review and shadow side
  effects), sync/async/streamed agent runs, agent escalation, batch decisions,
  pre-approval promotion, and manual SLA sweeps now expose their final sequence
  instead of stranding callers behind projection races. Focused Go suites, 217
  Vitest tests, 122 native browser journeys, and all 80 real-Wasm journeys pass.
- 2026-07-28: Closed the second whole-product audit release gate: `make ci`
  passes vet/build, strict lint, gosec, the complete race suite, deadcode, zero
  clone groups, zero reachable vulnerabilities, and licenses; the production web
  build and all 3 embedded-binary browser smokes pass. Generated demo/embed assets
  were verified clean after their guarded builds.
- 2026-07-28: Reconciled the completed second audit with authoritative
  `origin/main` at `fea7a26`, confirmed the PR queue was empty, committed the
  31-file journey completion as `379801f`, and opened PR #156 as the repository's
  sole review queue.
- 2026-07-28: Repaired PR #156 run 30305483664's sole failure:
  race-instrumented dual-replica SQLite projection work had outgrown the generic
  one-second async assertion (`A=19 B=18`). Ordinary projections retain the
  strict default while both deliberately heavy multi-replica proofs declare a
  bounded five-second deadline. The exact race case passes 30 consecutive runs,
  its helper regression passes, and the complete `make ci` gate is green.
- 2026-07-28: Babysat the repaired PR #156 through run 30306203748: all nine
  jobs pass, including Go race/security/license gates, web, 122 native browser
  journeys, 80 real-Wasm journeys, the embedded artifact, real PostgreSQL,
  Shauth SSO, Terraform, and container release. GitHub reports the PR cleanly
  mergeable.
- 2026-07-29: Confirmed PR #156 merged as `92b285f`, fetched authoritative
  `origin/main`, fast-forwarded the audit branch to that exact commit, verified
  the remote PR queue is empty, and opened the third whole-product journey audit
  with evidence-backed model-validation and issued-notice artifact gaps recorded
  in `DO_NEXT.md`.
- 2026-07-29: Made model validation a real independent approval gate:
  authenticated approver identity is authoritative, the owner cannot validate,
  current-version dataset/metric/notes evidence is mandatory, the latest result
  must pass, and Models/MRM/notifications/OpenAPI/seed history share the same
  contract. Focused command, HTTP, authorization, native-browser, and real-Wasm
  journeys pass.
- 2026-07-29: Made adverse-action issuance byte-reproducible: the exact artifact
  is appended with a command-derived SHA-256 hash, replayed into a separate
  projection, integrity-checked on exact download, and distinguished from the
  mutable current preview in the UI. HTTP e2e proves identity across settings,
  clock changes, replay, and tampering; native and real-Wasm journeys pass.
- 2026-07-29: Reconciled PLAN, ENTERPRISE, PERFORMANCE, JOURNEYS, OpenAPI, and
  BUGS with the shipped validation, durable human-task, measured durable-backend,
  and issued-artifact contracts; stale contradictory roadmap claims are gone.
- 2026-07-29: Closed the third-audit local release gate: `make check` and
  `make ci` pass vet/build, strict lint, SAST, every race test, deadcode, zero
  clone groups, zero reachable vulnerabilities, and licenses. Web formatting,
  zero-warning lint/typecheck, all 218 unit tests, production build, 122 native
  journeys, 81 real-Wasm journeys, and 3 embedded-binary smokes pass; generated
  assets and diff hygiene are clean.
- 2026-07-29: Re-fetched authoritative `origin/main` at `92b285f`, confirmed the
  GitHub PR queue is empty and the prior remote audit branch was deleted after
  merge, then committed the 34-file third-audit implementation as `4210abf`.
- 2026-07-29: Pushed the recreated
  `hardening/production-readiness-audit` branch and opened PR #157 as the
  repository's sole review queue; its body records both deliberate compatibility
  changes and the complete local verification matrix.
- 2026-07-29: Babysat PR #157 run 30486000491 through its terminal result: all
  nine jobs pass, including Go race/security/license gates, 122 native browser
  journeys, 81 real-Wasm journeys, the embedded artifact, real PostgreSQL,
  Shauth SSO, Terraform, web, and container release.
- 2026-07-29: Repaired the real-PostgreSQL CI race exposed by PR #157 run
  30486513300: `TestNodeTraceErasure` now waits for the exact assignment output
  it seals instead of any earlier projected node. Its 100-run race stress test
  and the complete `make ci` matrix pass.
- 2026-07-29: Governed operational policy activation end to end: immutable
  publication, independent reasoned approval/rejection, last-approved serving,
  exact decision/resume snapshots, role-queue notifications, API/OpenAPI,
  policy UI, replay-safe seed history, and native/real-Wasm two-actor journeys.
- 2026-07-29: Pinned governed AI nodes to immutable agent versions across the
  graph schema, provider port, exact historical config lookup, structured
  builder, templates, seed, and decision/preview validation; sandbox alone may
  explicitly select latest with version zero.
- 2026-07-29: Closed the fourth audit's product matrix: `make check`, formatting,
  zero-warning ESLint/Svelte, all 220 frontend unit tests, 124 native browser
  journeys, 81 real-Wasm journeys, and 3 embedded-production smokes pass.
- 2026-07-30: Closed the strict fourth-audit release gate: `make ci` passes
  vet/build, strict lint, SAST, every race test, deadcode, zero clone groups,
  zero reachable vulnerabilities, and dependency licenses.
- 2026-07-30: Re-fetched authoritative `origin/main` at `8622b7a`, confirmed
  the GitHub queue was empty, committed the 43-file governed-dependency slice
  as `333e63f`, pushed the recreated audit branch, and opened sole PR #158.
- 2026-07-30: Babysat PR #158 run 30491363960 to its terminal result: all nine
  jobs pass, including Go race/security/license gates, 124 native browser
  journeys, 81 real-Wasm journeys, the embedded artifact, real PostgreSQL,
  Shauth SSO, Terraform, web, and container release.
- 2026-07-30: Made shadow deployments candidate-faithful end to end: each graph
  resolves its own governed connector/AI/model dependencies over the same
  authoritative input and feature snapshot, policy-bound comparisons use exact
  governed outcomes, preview no longer persists consent, A/B cohorts remain
  stable, and version/policy/basis cohort boundaries plus errors and divergent
  decisions survive replay and are visible in the builder. Focused command,
  projection, HTTP, native-browser, and real-Wasm journeys pass.
- 2026-07-30: Made model actuals attributable performance evidence: the command
  derives probability, model version, and Predict-node lineage from a completed
  recorded decision; a tenant-wide claim rejects duplicate labels; ambiguous,
  stale, unknown, and spoofed evidence is rejected; model redefinition starts a
  fresh monitoring cohort and exposes any cross-replica stale exclusion.
  Command, projector/replay, HTTP, OpenAPI, native-browser, and real-Wasm
  journeys pass.
- 2026-07-30: Closed the fifth-audit local release matrix: `make check` and
  `make ci` pass vet/build, strict lint, SAST, every race test, deadcode, zero
  clone groups, zero reachable vulnerabilities, and dependency licenses;
  formatting, zero-warning ESLint/Svelte, all 221 frontend unit tests, 124
  native browser journeys, 83 real-Wasm journeys, and 3 embedded-production
  smokes pass.
- 2026-07-30: Re-fetched authoritative `origin/main` at `002e9d3`, confirmed
  the GitHub queue was empty, committed the 33-file fifth-audit slice as
  `c403f83`, pushed the recreated audit branch, and opened sole PR #159.
- 2026-07-30: Babysat PR #159 run 30505656168 to its terminal result: all nine
  jobs pass, including Go race/security/license gates, 124 native browser
  journeys, 83 real-Wasm journeys, the embedded artifact, real PostgreSQL,
  Shauth SSO, Terraform, web, and container release.
- 2026-07-30: Completed E1 durable execution integrity across the yielded-effect
  interpreter, idempotent/correlated decisions, cross-replica recovery,
  distributed async-agent attempts, crash-idempotent terminal side effects,
  API/SDK/UI/observability, and API/worker/scheduler deployment tiers; the
  complete contracts are recorded in `PLAN.md` §8b.2 and `BUGS.md` E1-1–E1-10.
- 2026-07-30: Closed the E1 local release matrix: `make check`, `make ci`, all
  18 Terraform contracts, Helm lint/render, 228 frontend units, 125 native
  browser journeys, 83 real-Wasm journeys over the 7,999-event demo history,
  and 3 embedded-production smokes pass; diff/SPDX hygiene is clean.
- 2026-07-30: Re-fetched authoritative `origin/main` at `7a7be66`, confirmed
  the GitHub PR queue is empty, and committed the 95-file E1 implementation as
  `8dddb44`.
- 2026-07-30: Pushed `enterprise/e1-durable-execution` and opened PR #160 as
  the repository's sole review queue, with the compatibility, topology-cost,
  behavioral-change, and complete local-evidence contract in its body.
- 2026-07-30: Babysat E1 PR #160 run `30514807479` to terminal green across all
  nine jobs: Go race/security/license gates, 125 native browser journeys, 83
  real-Wasm journeys, the embedded artifact, real PostgreSQL, real Shauth SSO,
  Terraform, web, and container-release contracts all pass.
- 2026-07-30: Verified E1 final head `c177798` repeated all nine hosted jobs
  green in run `30515124922`; PR #160 then merged as authoritative `fea2a7e`.
- 2026-07-30: Fetched and reconciled merged `origin/main`, confirmed the remote
  PR queue is empty, and cut `enterprise/e2-experiments-population` directly
  from `fea2a7e` for the complete `PLAN.md` §8b.3 vertical tranche.
- 2026-07-30: Implemented E2's governed experiment, stable reached-exposure,
  correctable business-outcome, statistical analysis, durable population-job,
  API/SDK/UI/notification, real-demo, and replay vertical; the assembled
  `TestExperimentOutcomeAndPopulationHTTPJourney` passes.
- 2026-07-30: Unified exact cohort evidence across fair-lending, corrected
  business-outcome model performance, and shadow comparisons; focused
  model/shadow/fair-lending/OpenAPI tests pass.
- 2026-07-30: Failure-tested population and experiment schedulers, fixed expired
  claims being blocked forever by their own concurrency slot, and proved two
  replacement replicas recover one killed-worker item with one successor claim
  and result; stop-window projection lag and idempotent result expiry also pass.
- 2026-07-30: Closed the E2 browser journeys: all 3 real-backend tests prove
  stable cohorts and corrected outcomes, durable backtest/result download, and
  production maker-checker launch; the walkthrough fixed missing dynamic-route
  packaging and made empty collecting analysis return complete array shapes.
- 2026-07-30: Reconciled PLAN/BUGS/JOURNEYS/ENTERPRISE/GAPS/COMPETITIVE and the
  component/public guides with the shipped governed experiment, generalized
  outcome, exact-cohort, and durable population contracts.
- 2026-07-30: Completed the local E2 matrix: `make check`, strict `make ci`, 232
  frontend units + production build, 128 native journeys, 83 real-Wasm
  journeys, 3 embedded smokes, 18 Terraform contracts, and the container
  release contract. CI-driven cleanup reached zero dead code and zero clone
  groups; the assembled E2 journey passes five consecutive race runs across
  projection lag.
- 2026-07-30: Pushed the 79-file E2 implementation as `0eaa5a5`, opened sole
  PR #161, and babysat hosted run `30527793916` to terminal green across all
  nine jobs, including real PostgreSQL, real-Wasm, SSO, and release contracts.
- 2026-07-30: Verified final E2 head `0ea67ec` repeated all nine hosted jobs
  green in run `30528560455`; PR #161 then merged as authoritative `565e1d2`.
- 2026-07-30: Fetched and reconciled merged `origin/main`, confirmed the E2
  remote branch is deleted and the PR queue empty, and cut
  `enterprise/e3-case-operations` directly from `565e1d2`.
- 2026-07-30: Audited the complete Case Manager vertical and recorded six E3
  failure groups: unversioned types, absent routing, absent evidence lifecycle,
  browser-fanned bulk work, absent independent QA, and incomplete operations.
- 2026-07-30: Implemented the first E3 core: versioned definitions and business
  calendars, pinned governed opens, deterministic replica-safe routing,
  evidence/attachment governance, dispositions, saved views, independent QA,
  projections and HTTP/RBAC contracts; focused tests and whole-repo compile pass.
- 2026-07-30: Completed E3 lifecycle truth: dynamic governed states replay,
  status/disposition share one terminal claim, required second review keeps work
  open until independent agreement/override, and validated outcomes exclude
  unresolved disagreement; concurrency and replay tests pass.
- 2026-07-30: Completed E3 operations: ordered attribute/priority/age routing,
  restart-reconciled atomic SLA queue escalation, capacity-safe rebalance,
  durable bounded idempotent bulk manifests, search/saved views/opaque duplicate
  groups, analytics, webhook retry/dead-letter history, and notifications;
  focused domain/command/cases/scheduler tests pass.
- 2026-07-30: Completed E3 evidence/privacy boundaries: linked evidence,
  required-evidence dispositions, immutable attachment metadata, lawful-basis
  and retention/hold/erasure annotations, PII masking, audit export, and
  capability-redacting reads whose purpose-bound access command records one
  event before returning the external pointer; direct handler regression passes.
- 2026-07-30: Completed E3 product surfaces: full Go/TypeScript SDK contracts,
  OpenAPI/RBAC, type/queue/reviewer administration, role-layout case detail,
  saved views, duplicate review, four backend-owned bulk operations,
  evidence/attachment/QA/webhook workflows, analytics/export, and 8,708-event
  governed real-demo history; frontend lint/typecheck and repository compilation
  are green.
- 2026-07-30: Closed the final E3 journey audit: dynamic terminal states now
  stop SLA/routing/workload/notifications and render from `resolved_at`; lifecycle
  claims order sweeps against closure; and layout-declared editable fields now
  have typed role authorization, CAS serialization, API/SDK/UI support, replay,
  and concurrent-editor/browser regressions.
- 2026-07-30: Closed the E3 multi-replica and notification races: a suspended
  decision now owns every case lifecycle transition until resume, queue/reviewer
  validation is deterministic, and resolved cases permanently suppress late
  assignment and SLA tasks; focused concurrency/replay regressions pass.
- 2026-07-30: Closed the real-browser case audit: zero lifecycle timestamps are
  omitted from the wire instead of making active work appear closed, numeric
  typed fields accept Svelte's runtime number binding, and all 130 native
  Playwright journeys pass over the assembled Go API.
- 2026-07-30: Completed the first E3 release matrix pass: `make check` and
  `make ci` pass with strict lint, SAST, race, zero dead code, zero clone groups,
  zero reachable vulnerabilities, and licenses; frontend formatting, lint,
  zero-warning typecheck, 232 units, and production build pass; the regenerated
  8,708-event seed round-trips through the real backend.
- 2026-07-30: Closed the complete local E3 release matrix at the final source:
  130 native and 83 real-Wasm browser journeys, 3 embedded-production smokes,
  Helm lint/render, 18 Terraform contracts, bounded-workflow validation,
  container publication/retention, and diff/SPDX/OpenAPI/generated-asset
  hygiene all pass.
- 2026-07-30: Reconciled fresh `origin/main` at `565e1d2`, confirmed the GitHub
  review queue empty, pushed E3 commits `1b9e8bf` and `ec8f756`, and opened sole
  PR #162 with hosted run `30545840816`.
- 2026-07-30: Diagnosed final-head run `30545905179`'s hosted-only race failure:
  the assembled E2 journey transitioned a durably created experiment before its
  projection-backed GET became visible; aligned the test with the explicit
  eventual-read contract before transition. The exact regression passes 50
  consecutive race runs and the complete local Go gate passes.
- 2026-07-30: Followed the hosted E2 projection-race cascade through running
  state, corrected outcome history, exposures, and analysis; injected one
  deterministic journey clock and strengthened the assertion to prove the
  corrected in-window cohort. The exact journey passes 100 consecutive race
  runs; full `make ci` passes strict lint/SAST/race/dead-code/clone/vulnerability/
  license gates.
