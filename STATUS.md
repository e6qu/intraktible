# STATUS — where the work stands right now

**Read this first** after any compaction or new session, then `DO_NEXT.md`.

## Task

Enterprise capability audit: compare the proposed whole-product scope and every
key Builder, Developer, Operator, Manager, Product, Executive, Evaluator, and
Admin journey with the implementation and Taktile's currently published product
surface. Distinguish working foundations from enterprise-depth gaps, then order
the next implementation tranche by correctness and operational risk.

## Standing rules (user-issued, non-negotiable)

1. **SERIALIZED BIG/HUGE PRs E1–E7.** Each enterprise tranche is one large
   vertical PR. Never keep more than one PR open; merge the current tranche,
   fetch and reconcile authoritative `origin/main`, then cut the next branch.
   Do not fragment a tranche into anemic PRs.
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

**Enterprise PR E1 — durable execution integrity is implementation-complete on
`enterprise/e1-durable-execution` from merged `origin/main` commit `7a7be66`.**
PR #159 is merged and the remote PR queue was empty when the branch was cut, so
the one-open-PR rule is satisfied. The planning edits and implementation will
ship together as one fat vertical PR. Local closeout is green; the remaining
step is to re-fetch authoritative remote state, commit, push, open the sole PR,
and babysit its required checks before E2 can begin.

The product has a broad, real foundation across the Decision Engine, Context
Layer, Case Manager, Agent Manager, governance, compliance, and deployment
surfaces. This audit found four enterprise-blocking semantics that cut across
otherwise complete journeys: effectful graph nodes were resolved eagerly before
graph traversal instead of only when reached; decision requests lacked
idempotency and ignored their advertised metadata/control fields; A/B assignment
is random per invocation rather than stable by subject; and every API replica
could recover the same unfinished async agent run without a distributed claim.
E1 closes the first, second, and fourth blockers; stable experiments remain the
deliberately serialized E2 scope.
`PLAN.md` §8b now carves the remaining enterprise program into seven strictly
serialized large-to-huge vertical PRs (E1–E7), each with full-stack scope and
exit evidence. E1 has replaced eager effect preparation with a pure resumable
interpreter: Connect/AI/Predict are requested only when traversal reaches them
and receive the exact upstream-mutated record. The shell records deterministic
requested/succeeded/failed effect evidence and preview uses the same execution
path with explicit preview-only mocks. Decision invocation now has a durable
idempotency claim and request hash, business reference, correlation id, bounded
metadata, entity tracking, timeout control, a finalized marker, and history
search dimensions. Concurrent retries are covered at command level. Recovery
ownership and provider delivery semantics are now durable: recovery uses
cross-replica lease claims, reuses recorded successes, passes stable provider
idempotency keys, and abandons an indeterminate at-least-once effect rather than
duplicating it. Async agent runs now have durable idempotent admission,
version/timeout/attempt controls, distributed claims and heartbeats,
cancellation, explicit retry, timeout, and dead-letter outcomes. Tenant-safe
polling and terminal counters are replay-correct. The native binary and Helm
topology now distinguish `api`, horizontally scalable `worker`, singleton
`scheduler`, and local `all` roles; only workers execute durable decision/agent
work. Manual review, shadow, preapproval, and consent terminal-adjacent effects
are crash-idempotent. HTTP, OpenAPI, Go/TypeScript SDK, history indexing,
observability, and decision/agent operator UI contracts are complete. Focused
command, service, route, frontend-unit, and native-browser tests are green.
Counterfactual analysis now replays recorded live effects without provider I/O;
Coverage requires explicit record-free mocks and exposes failed synthetic runs;
policy evaluation errors fail decisions rather than silently converting to
referrals. Helm lint/render and all 18 Terraform plan-contract tests prove the
three-tier topology. Pre-E1 event compatibility is deterministic and
provider-free. The complete local E1 matrix passes: `make check`; `make ci`
(strict lint, SAST, race, dead-code, zero clone groups, zero reachable
vulnerabilities, licenses); frontend formatting, zero-warning typecheck/lint,
228 unit tests; 125 native browser journeys; 83 real-Wasm journeys over a
regenerated 7,999-event history; and 3 embedded-production smokes. Diff hygiene
and SPDX checks are clean.

A closeout fetch on 2026-07-30 confirms `HEAD` and `origin/main` are still
exactly `7a7be6607d81e52c5b924da8ad898a1a9521fcf2`; the prior remote audit branch
is deleted and `gh pr list --state open` is empty. There is therefore no remote
work to reconcile before the E1 commit.

The 95-file E1 slice is commit `8dddb44` (`Make decision and agent execution
durable`) with continuity evidence in `635ca00`. Both are pushed in PR #160,
the repository's sole open review queue. Required checks are pending.

**Fifth whole-product journey audit — complete in sole PR #159.**
The audit began after PR #158 merged as `002e9d3`, with an empty GitHub queue
and the refreshed `hardening/production-readiness-audit` branch exactly at
`origin/main`. PR #159 is now the repository's sole open review queue. The
fourth audit's governed policy snapshots and immutable agent version pins were
the authoritative baseline.

The walk found and closed two concrete cross-layer evidence defects. Shadow
evaluation now independently resolves candidate-selected connector/AI/model
dependencies from the same caller input and authoritative entity-feature
snapshot, retains consent/egress and governed version/approval gates, compares
the exact selected policy outcome when one is bound (otherwise the full output),
and starts a new replay-safe cohort whenever the live version, candidate,
comparison basis, or selected policy changes. Candidate errors and explanatory
divergences are durable and visible; preview no longer persists consent; and
A/B selection no longer destabilizes champion shadow cohorts.

Model actuals are no longer caller-authored probabilities. A realized label
must reference a completed recorded decision, and the command derives the exact
Predict-node probability and historical model version from replayable evidence.
Ambiguous, stale, unknown, spoofed, or duplicate lineage is rejected, with a
tenant-wide permanent claim enforcing one outcome per decision/model/node
across replicas. Model redefinition begins a fresh monitoring cohort; a narrow
cross-replica stale append is excluded explicitly rather than blended.
The API, OpenAPI, Models UI, projections, replay, and help journey all share the
same contract. `DO_NEXT.md` has no open implementation item.

That inventory is complete. Every documented journey retains a concrete UI or
API entry and supporting end-to-end coverage. The two mutable decision
dependencies it found are now closed: policy activation is independently
governed and snapshotted per decision, while governed AI nodes pin an immutable
agent version so registry edits cannot bypass a reviewed flow deployment.
`DO_NEXT.md` has no open product item.
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

The fourth audit's governed-dependency slice is implemented across the native
core, HTTP surface, UI, notifications, and seed source. Policy publication is
now immutable sandbox iteration, while staging/production retain the last
four-eyes-approved version; a decision records its selected policy before
execution and an eventual resume reuses that exact version. AI nodes carry an
immutable agent version, and both recorded decisions and previews refuse
unversioned AI nodes outside sandbox. Focused command, projection/replay,
assembled HTTP, agent-provider, notification, authorization, OpenAPI, Svelte,
and frontend-unit tests are green. Deterministic demo regeneration, the
race-enabled repository check, all 124 native browser journeys, all 81
real-Wasm journeys, and all 3 embedded-production smokes are green. The complete
43-file slice is commit `333e63f` in PR #158, the repository's sole open PR.
Run 30491363960 is terminal green: all nine jobs passed.

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

**Current local gates, all green:** the fifth-audit `make check` and `make ci`
pass vet/build, strict lint, SAST, the complete race-enabled Go suite, deadcode,
zero clone groups, zero reachable vulnerabilities, and dependency licenses.
Prettier, zero-warning ESLint and Svelte check, all 221 Vitest tests, 124 native
Playwright journeys, 83 real-Wasm journeys, and 3 embedded-binary smokes also
pass. The implementation is commit `c403f83` in sole PR #159. Remote run
30505656168 is terminal green: all nine jobs pass, including Go,
real PostgreSQL, native and real-Wasm browser journeys, the embedded artifact,
Shauth SSO, Terraform, web, container release, security, and licenses.

## Ground truth established so far

- Current branch `hardening/production-readiness-audit` is based on PR #158's
  merge commit `002e9d3`; a fresh fetch confirms it exactly matches
  `origin/main`.
- PR #158 is merged and the remote PR queue is empty — satisfies the
  one-open-PR rule at the start of this round.
- Fourth-audit product and artifact gates are green: policy replay/governance,
  the complete Decision Engine package, assembled policy HTTP serving, exact
  resume snapshots, immutable agent dispatch, notifications, route
  authorization, OpenAPI drift, `make check`, zero-warning Svelte/ESLint,
  220 Vitest tests, 124 native browser journeys, 81 real-Wasm journeys, and 3
  embedded-production smokes all pass. `make ci` also passes strict Go lint,
  SAST, deadcode, zero clone groups, zero reachable vulnerabilities, and
  dependency licenses. PR #158 run 30491363960 repeats the matrix remotely:
  container release, demo/Wasm, native and embedded e2e, Go, real PostgreSQL,
  Shauth SSO, Terraform, and web all pass.
- Fifth-audit shadow evidence is independently resolved for the candidate,
  cohort-dimensioned, replay-safe, policy-aware, and surfaced with errors and
  explanatory decision links. Model actuals derive their prediction lineage
  from the authoritative event log, are duplicate-safe across replicas, and
  cannot blend across model versions. Focused core, HTTP, projection/replay,
  OpenAPI, native UI, and real-Wasm tests cover both journeys.
- The fifth-audit complete local matrix passes: `make check`, `make ci`, all 221
  frontend units, 124 native browser journeys, 83 real-Wasm journeys, and 3
  embedded-production smokes.
- Commit `c403f83` is pushed in sole PR #159. Remote run 30505656168 is
  terminal green across all nine required jobs.
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
