<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# STATUS — where the work stands right now

**Read this first** after any compaction or new session, then `DO_NEXT.md`.

## Task

Turn the whole-product capability audit into the detailed plan-of-record: map
every key Builder, Developer, Agent Designer, Reviewer, Operator, Modeler,
Validator, Admin, SRE, Domain Owner, and Executive journey to what is real and
what remains, then carve the remaining work into serialized large-to-huge
full-stack pull requests. The next implementation tranche is E4, collaborative
authoring and governed delivery.

## Standing rules (user-issued, non-negotiable)

1. **SERIALIZED BIG/HUGE PRs E1–E8.** Each enterprise tranche is one large
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

**Enterprise PR E3 — enterprise case operations is merged.** Authoritative
`origin/main` is merge commit `89bca6a`, containing E3 head `7e55c3d`; its final
hosted run `30547883192` was green across all nine jobs. Fresh reconciliation
found no open PR before `enterprise/e4-collaborative-authoring` was cut exactly
from that merged main.

The whole-product audit is now represented directly in `PLAN.md` §8b. It defines
the complete enterprise operating loop, maps ten persona journeys to the
working foundation and remaining enterprise-depth gaps, and records five
remaining serialized verticals:

- **E4:** collaborative authoring and governed delivery;
- **E5:** governed agentic operations and human/AI learning;
- **E6:** model and context data-science platform;
- **E7:** production scale, tenancy, and disaster recovery; and
- **E8:** ecosystem and regulated solution packs.

Each tranche has a starting boundary, primary journeys, domain/event/API/UI/
worker/security/migration scope, explicit non-goals and dependency gates, and
end-to-end exit evidence. Agent operations is deliberately its own E5 tranche:
the existing agent runtime/eval foundation is real, but reusable specialist
agents, adversarial/repeated evaluation, untrusted-content containment,
governed deployment, evidence-cited case assistance, and outcome learning form
a complete product loop too large to hide inside model/data science.

E4 implementation is in flight on the branch. The first vertical core now
contains event-sourced revisioned drafts, optimistic save conflicts with the
authoritative snapshot, disposable presence leases, immutable reusable
component versions with recursive exact-pin expansion/cycle rejection and a
consumer index, server-side semantic graph diff, governed changesets with
required checks and independent review, crash-reconciled publication, and
published source/dependency/draft lineage. Changesets retain the exact source,
schema, compiled dependency pins, and base material reviewed even when the
working draft advances; incomplete topology is accepted while editing and
rejected at the changeset validation boundary. The server, scheduler,
projector, HTTP/OpenAPI routes, Go/TypeScript SDKs, notification tasks, reusable
component registry, and durable autosaving/conflict/review builder surfaces are
wired. Versioned byte-stable canonical sources now import idempotently into
validated drafts instead of publishing; the CLI drives validation/import/status/
diff/impact/submission/publication, and the scheduler owns overdue review
reminders plus protected stale-draft retention. Server-derived component
compatibility, explicit breaking-change evidence, compatible consumer-upgrade
drafts, create-key plus natural-resource retry idempotency, and governed
real-demo history are implemented. Canonical bundle preflight now resolves and
compiles every member before the first append; imports preserve explicit future
format versions so unsupported semantics fail instead of being normalized down,
and portable component-id rewrites are returned as a deterministic migration
report. Canonical export fails closed on workspace-classified embedded
fixtures, while review evidence/reasons and comments reject high-signal
free-text PII before it reaches the log. Four real-browser journeys prove
competing-editor recovery, exact maker/checker review with notification refresh
and environment impact, durable revision restore/archive, and
reusable-component consumer upgrades with visible pinned runtime contents. That
walkthrough also aligned changeset validation with publication's runtime dry
compiler. Comment resolution/reopen is event-sourced and replayed; the Go and
TypeScript SDKs cover draft history/rebase/archive, component lifecycle,
changesets, and presence. Promotion enforcement now reads the just-recorded
policy from the authoritative flow stream instead of racing its projection; a
deterministic stale-projection service regression and ten browser repetitions
with retries disabled prove the maker-checker handoff. Review submission also
drains an older autosave and persists any newer editor fingerprint before
pinning its revision, and the editor stays non-interactive until flow metadata
and its exact draft are committed together. Twenty parallel retry-free browser
repetitions prove a just-added node cannot be overwritten or omitted from
review. Legacy UI exports are normalized into the canonical governed import
contract without forwarding target-owned publication metadata, while
canonical-marked documents remain strict so unknown fields still fail loudly.
The regenerated 8,729-event demo history and governed template/import journeys
use the same review path as production. The complete verification matrix is
green: 134 retry-free native browser journeys, 84 real-Wasm journeys, 4
embedded-binary journeys, 240 frontend units, zero-warning typecheck/lint,
production web build, full race-enabled Go CI with zero reachable
vulnerabilities, Terraform tests, container publication/retention checks, and
workflow timeout checks. Final diff/documentation review and the sole E4 PR
remain.
E5–E8 must not start or open concurrently.

The first E4 seam audit establishes the implementation boundary:

- `web/src/routes/engine/[flowId]/+page.svelte` keeps `editNodes`,
  `editEdges`, and the saved fingerprint in browser memory, warns on navigation,
  and explicitly says the canvas is in-memory until Publish. Reload/device
  change loses the work; there is no revision or conflict contract.
- `POST /v1/flows/{flow_id}/versions` validates and appends an immutable
  version directly. Existing JSON import uses the same immediate publication
  path. Production deployment still has maker-checker, but authoring evidence,
  discussions, semantic diff, and review do not form one pinned changeset.
- `web/src/lib/diff.ts` compares two published graphs only in the browser.
  There is no canonical server diff or dependency impact contract.
- `platform/comments` provides real durable subject threads and mentions, but
  it has no object address, review assignment, required-check, or changeset
  lifecycle semantics.
- The flow graph has no reusable/subflow node, component registry, dependency
  pins, expansion, cycle detection, consumer index, or exact component lineage.

The chosen core is a new event-sourced authoring vertical: full-snapshot
revisioned drafts with optimistic claims; disposable store-backed presence
leases; changesets pinned to draft/base/dependency revisions; immutable typed
component versions; pure canonicalization/diff/expansion; and a publish bridge
that records exact source/dependency lineage while preserving the compiled
runtime graph. No dependency is required for this core.

**Enterprise PR E2 — experimentation, outcomes, and population automation is
merged as authoritative commit `565e1d2`.** Its implementation commit
`0eaa5a5` passed all nine hosted jobs in run `30527793916`; final evidence head
`0ea67ec` repeated the complete matrix in run `30528560455` before merge.

The E2 core is now implemented end to end: governed experiment lifecycle and
maker-checker launch, deterministic exact-version assignments and reached
exposures, correctable decision-linked outcomes with derived treatment/model
lineage, reproducible statistical safety states, durable version-pinned
population jobs, worker leases, scheduler retention, API/SDK/UI surfaces, real
demo history, notifications, and assembled HTTP replay coverage. Cross-product
cohort alignment now includes fair-lending slices, corrected business-outcome
model performance, and retained exact shadow cohorts. Failure injection found
and fixed a production recovery defect where an expired population claim still
consumed the concurrency slot and could never be reclaimed; two racing
replacement replicas now produce one successor claim and one result. Window
scheduling also reconciles the event aggregate across projection lag. The real
browser now proves stable repeated-subject assignment,
reached-exposure drill-down, outcome recording/correction, underpowered
no-winner presentation, durable backtest completion/result download, and
production maker-to-independent-approver launch. That walkthrough also found
and fixed missing SPA packaging for both dynamic detail routes and a null
collecting-analysis shape. Focused Go tests for models, shadow, decisions,
experiments, population, fair lending, notifications, and OpenAPI are green.
The plan-of-record, issue ledger, component guide, honest gap/competitive
analysis, and persona journeys now describe the implemented first-class
experiment/outcome/population contracts instead of legacy request-time
challengers or model-only actuals. Authoritative remote reconciliation confirms
`origin/main` remains `fea2a7e`, the remote E2 head matches the local
implementation commit, and PR #161 is the sole review.

The local release matrix is now green: `make check`; `make ci` (strict lint,
SAST, race, dead-code, zero clone groups, zero reachable vulnerabilities, and
licenses); frontend formatting/typecheck/lint, 232 units, and production build;
128 native browser journeys; 83 real-Wasm journeys; 3 embedded-binary smokes;
18 Terraform contracts; and the container release contract. CI itself found
and drove fixes for six lint findings, four clone groups, twelve dead exports,
and a race-only assembled-journey assumption that a durable population create
was already projected. The latter now polls the explicit eventual-consistency
boundary and passes five consecutive race runs. A local reproduction of the
real-PostgreSQL job could not start because Docker Desktop returned
`unable to upgrade to tcp, received 500` after pulling the repository's pinned
`postgres:16` image; the exact disposable Created container was removed. Hosted
run `30527793916` closes that evidence gap and is terminal green across all nine
jobs: Go race/security/dead-code/clone/vulnerability/license gates, native UI,
real-Wasm demo, embedded artifact, web, real PostgreSQL, real Shauth SSO,
Terraform, and container-release contracts.

**Enterprise PR E1 — durable execution integrity is implementation-complete on
`enterprise/e1-durable-execution` from merged `origin/main` commit `7a7be66`.**
PR #159 is merged and the remote PR queue was empty when the branch was cut, so
the one-open-PR rule is satisfied. The planning edits and implementation will
ship together as one fat vertical PR. Local closeout and the first complete
hosted matrix are green; PR #160 is the repository's sole review queue and E2
must wait for its merge.

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
`PLAN.md` §8b originally carved the enterprise program into seven strictly
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
durable`) with continuity evidence in `635ca00`. Both merged through PR #160.
Hosted run `30514807479` is terminal
green across all nine jobs: Go race/security/dead-code/clone/vulnerability/
license gates, native UI, real-Wasm demo, embedded artifact, web, real
PostgreSQL, real Shauth SSO, Terraform, and container-release contracts; final
head `c177798` repeated that matrix in run `30515124922` before merge.

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
