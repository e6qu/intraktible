# STATUS — where the work stands right now

**Read this first** after any compaction or new session, then `DO_NEXT.md`.

## Task

Whole-product journey audit: step back from individual endpoints and verify that
each key Builder, Developer, Operator, Manager, Product, Executive, Evaluator, and
Admin journey makes sense end to end across the UI/UX, HTTP boundary, command/event
path, projections, schedulers, notifications, restart/replay, and deployment shape.
Fix every missing or misleading piece found; do not stop at documenting gaps.

## Standing rules (user-issued, non-negotiable)

1. **ONE BIG FAT PR.** Branch `hardening/production-readiness-audit`, one PR at
   the end. Phased work internally is fine; splitting into several PRs is not.
   No mid-work checkpoint that waits for approval.
   **Never open a second PR.** While that PR is open and unmerged, every further
   change — including anything the user asks for next, related or not — is a new
   commit on the SAME branch, which updates the SAME PR. Do not ask whether to
   open another one; the answer is always no until the open PR is merged.
2. **NO FALLBACKS.** No `catch{}` / log-and-continue / degraded mode / backup
   path. A branch that only runs when something is already broken is banned —
   fail loudly, as early as possible. NOT fallbacks: network retries, explicit
   configured defaults, normalizing a documented `omitempty` shape on a 2xx.
3. **FIX EVERYTHING FOUND, even if it looks unrelated.** My "unrelated" judgment
   is unreliable — I lack the repo's history. No follow-up deferrals. Follow fix
   cascades to the bottom. Flag suspected-deliberate changes in the PR body
   _after_ fixing, never instead of fixing.
4. **BOY SCOUT RULE.** Every file opened — even only to read — gets left better.
   Not a licence for style churn or renames.
5. **DON'T FAKE IT, MAKE IT REAL.** A stub becomes a real implementation or an
   explicit loud error. Never a nicer-looking stub.
6. No subagents, no workflows (system-prompt instruction).

## Phase

**Third whole-product journey audit — complete on PR #157.**
PR #156 merged as `92b285f`; PR #157 is the sole open PR, and the refreshed
`hardening/production-readiness-audit` branch started exactly at `origin/main`.
`docs/JOURNEYS.md` is being treated as an executable product contract rather
than endpoint documentation. The architecture/continuity/component plans are
read and remote `main` is authoritative and clean. Three complete slices are now
fixed: every retention sweep honors statutory holds; future production
deployment is maker-checker governed with atomic scheduler transitions; and
case SLA webhook outcomes are durable, retryable across restart, and visible in
the case UI. Agent escalation is also now one durable case per run across
reload/replay/concurrent retries, and async admission is actually bounded. The
notification bell now receives role-gated operational and approval events, with
independent read state for shared queues. Admins can now operate retention
policy/sweeps and execute the guarded hold→release→erasure journey from the real
UI. Background scheduler failures now fail both probes until a good tick proves
recovery, and the model-monitoring actuals dead end is closed. Scheduled
deployment cancellation is now an atomic state-aware transition, and the real
two-actor browser journey proves a future production request moves from maker
notification through independent approval into a pending scheduler-owned
window without deploying early. Durable human tasks now close coherently:
suspension and case share one recorded id, premature generic case completion is
refused, a reviewer outcome resumes the decision and completes the linked case
through the same replayable event, and resolved task notifications leave every
reviewer's inbox. Model approval is also now a complete handoff: Manager views
count both flow and model requests, the shared notification opens the exact model
Governance panel, and decided flow/model requests disappear from every
approver's actionable inbox. The journey and launch guides are reconciled
against the implemented scheduler/governance contracts, and the flow
authoring UI explicitly distinguishes the in-memory canvas draft from the
published/deployed version exercised by Test. That target is enforced: no run
before first publish, and staging/production—including Preview—require their
governed deployment rather than falling through to latest.
Cross-subject collaboration is now complete as well: policy mentions, previously
inert, and model mentions, previously landing on an undifferentiated registry,
open the exact subject and discussion thread; model approvals still open the
exact Governance panel.

The second pass completed its two slices. Context data is
fully operable from the browser: entity create/update, event recording, feature
recomputation, connector validation, and durable fetch evidence all use the real
backend. The regulated-decision arc is coherent from fair-lending configuration
and a reason-coded decline through notice issuance, contest, human
reconsideration, and Compliance queues. Lawful-basis expiry is evaluated by the
backend clock and represented consistently by entity and Compliance UIs. The
browser now also has a single read-after-write contract across the whole
event-sourced product: commands that return a durable sequence wait for the
projection watermark before the UI reloads, including decide/resume, batch,
agent run/stream/escalation, and SLA sweep paths that previously omitted their
final sequence. The second inventory closed with a complete green repository
and shipping-artifact matrix in PR #156. The new pass has identified two
evidence-backed cross-layer gaps: model approval does not require
current-version independent validation and permits self-attested validation;
adverse-action issuance stores a hash but not the exact notice bytes, so
settings or clock changes can make the downloadable rendering diverge from the
issued artifact. Both are now fixed across the command/event/projection/API/UI
paths. Model approval requires substantive passing evidence from an
authenticated actor other than the model owner; the exact adverse-action bytes
are retained, hash-verified, replayable, and downloaded separately from the
current preview. `DO_NEXT.md` has no open implementation items.

**Current local gates, all green:** `make check` and `make ci` pass vet/build,
strict lint, SAST, the complete race-enabled Go suite, deadcode, zero clone
groups, zero reachable vulnerabilities, and dependency licenses. Prettier,
zero-warning ESLint and Svelte check, all 218 Vitest tests, the production web
build, 122 native Playwright journeys, 81 real-Wasm journeys, and 3
embedded-binary smokes also pass. Fresh `origin/main` still equals the local
base `92b285f`; the implementation is committed as `4210abf` and PR #157 is the
repository's sole review queue. Run 30486000491 passes all nine jobs, including
Go, web, native browser, real-Wasm, embedded artifact, real PostgreSQL, Shauth
SSO, Terraform, and container release. Final continuity-head run 30486513300
then exposed a pre-existing node-trace e2e race under the loaded PostgreSQL
matrix: it waited for any projected node before asserting a later node. The
predicate now waits for the exact assignment output under test; its 100-run
race stress test and the complete local `make ci` gate pass. The prior merged
baseline remains independently proven by PR #156 run 30306203748 across the
same deployment-specific matrix.

## Ground truth established so far

- Current branch `hardening/production-readiness-audit` is based on PR #156's
  merge commit `92b285f`; a fresh fetch confirms it exactly matches
  `origin/main`.
- PR #156 is merged and the remote PR queue is empty — satisfies the
  one-open-PR rule.
- The model-validation endpoint now takes validator identity only from the
  authenticated actor, is approver-gated, rejects the model owner, requires
  substantive evidence, and approval requires the latest independent
  current-version record to pass. Command, HTTP, authorization, MRM, native UI,
  and real-Wasm tests exercise the handoff.
- Adverse-action issuance now retains the exact served bytes and
  command-derived hash in the append-only event, projects content separately
  from list metadata, and verifies both before exact download. HTTP e2e proves
  byte identity after settings/date changes and fresh replay, and refuses a
  tampered projection.
- `make check` and `make lint` pass; the real-Postgres claim contract and full
  event-log race suite pass.
- This round passes `make check`, `make lint`, and a dedicated complete
  `platform/eventlog` race run. The expiry regression was verified to fail
  before the fix (`re-claim ... returned <nil>, want ErrConflict`).
- The current whole-product diff passes the full race check, scheduler
  extraction tests, strict lint/SAST/deadcode/licenses, and zero clone groups;
  `govulncheck` reports zero reachable vulnerabilities after the approved
  gRPC-only security update.
- Toolchain present locally: Go 1.26.5, Node v26.5.0, Docker 29.6.1.
- Whole-product audit focused gates so far: `go test ./platform/erasure
./server`, decision-engine/command/flows/schedule tests,
  `TestScheduleDeployOverHTTP`, `TestScheduledProductionDeploymentOverHTTP`,
  case-manager plus server tests including durable SLA retry/replay, and
  the complete agent-manager suite including cross-handler escalation and queue
  saturation, notifications/monitor/service/server tests including shared read
  replay and role queues, plus `npm run check` (zero Svelte errors/warnings).
  Data-governance client/unit coverage (74 API tests) and the focused real-server
  Playwright compliance/entity journeys (5 tests) also pass.
  Focused scheduler/health tests pass across server, monitor, model drift,
  deployment, case SLA, and retention packages.
  The complete Decision Engine suite, zero-warning Svelte check, and all 4
  Models Playwright tests pass after the actuals-reconciliation slice.
  The focused builder journey proving dirty-draft warning → publish → run of the
  persisted version also passes (3 Playwright tests), as do the complete
  Decision Engine suite and the focused non-sandbox target regressions; Svelte
  remains at zero errors/warnings.
  All focused Policy + Notifications journeys, both model notification handoffs,
  and all 211 Vitest tests pass after the collaboration fix.
  In the second pass, focused Context/consent/server Go tests, 79 API/poll unit
  tests, zero-warning Svelte and ESLint checks, and all 12 Context Data +
  Decisions real-server Playwright journeys pass. After the cross-product
  consistency repair, all 217 Vitest tests, 122 native Playwright journeys, and
  all 80 real-Wasm journeys pass.
  The final `make ci` passes vet/typecheck, strict lint, gosec, the complete
  race-enabled Go suite, deadcode, zero clone groups, zero reachable
  vulnerabilities, and dependency licenses. The production web build and all 3
  embedded-binary smokes pass.

## Verdicts on the user's headline questions (evidence-backed)

- **Is the backend real?** YES. `server/server.go` is a genuine composition root
  wiring real command handlers → event log → projections → HTTP across all four
  components. 77k lines of Go, not scaffolding.
- **Is the frontend really connected?** YES. Pages import real API functions
  from `$lib/api` (multi-line imports; a naive one-line grep misses them and
  falsely reports "NO-API"). `web/src/lib/api.ts` (3754 lines) is a real REST
  client that throws on non-2xx via `errorOrStatus` rather than returning empty
  data. The `mock` hits under `web/src/routes/` are comments about the _wasm
  demo's_ fetch bridge, not mocks in the served app.
- **Production/multi-replica?** Real design work was already there (single-writer
  projection checkpoint, Helm API tier + singleton scheduler with
  `strategy: Recreate`, HPA/PDB, probes) — but four defects sat on exactly the
  recommended deployment shape and all four are now fixed: the Postgres log lost
  events under concurrent appends, the launch origin did not fail closed,
  `--log=file` warned instead of refusing, and SIGTERM dropped requests for want
  of a readiness drain. See `WHAT_WE_DID.md`.
- **CI?** Comprehensive on paper, and two of its gates were not gating: the
  `postgres` job ran a hand-written package list that omitted a Postgres test
  which had therefore never executed in CI, and `make e2e-embedded` — the only
  check of the artifact `release.yml` publishes — was a local pre-commit hook
  only. Both fixed.
