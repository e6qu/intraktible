# STATUS — where the work stands right now

**Read this first** after any compaction or new session, then `DO_NEXT.md`.

## Task

Thorough production-readiness review of intraktible **and fix everything found**:
what's missing, what's fake, is the backend real, is the frontend truly wired to
it, can this be released/deployed in production (load balancer + multi-replica
backend), does CI actually gate. Plus GitHub issue **#144**.

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
   *after* fixing, never instead of fixing.
4. **BOY SCOUT RULE.** Every file opened — even only to read — gets left better.
   Not a licence for style churn or renames.
5. **DON'T FAKE IT, MAKE IT REAL.** A stub becomes a real implementation or an
   explicit loud error. Never a nicer-looking stub.
6. No subagents, no workflows (system-prompt instruction).

## Phase

**Phase 2 — fixing, largely done.** Nine defects found and fixed across the
production path, CI, and the frontend; see `WHAT_WE_DID.md`. Two items remain in
`DO_NEXT.md` (the shauth-sso gate has not been run locally; the new append lock
is unmeasured), plus an unfinished sweep list.

**Gates run locally, all green:** `go build`, `go test ./...`, `go test -race`
against real PostgreSQL 16 (whole suite), lint, gosec, deadcode, dupl,
govulncheck, licenses, prettier, eslint, svelte-check, vitest (207),
Playwright e2e (97), Playwright wasm demo (80), `make e2e-embedded` (3),
`make terraform-check` (16), `make container-release-check`.
**Not run locally:** the `shauth-sso` job — needs a Shauth checkout + Ory Hydra.

## Ground truth established so far

- Base commit `292d882` (origin/main). Branch created off it. Tree was clean.
- No other open PRs (`gh pr list` empty) — satisfies the one-open-PR rule.
- `go build ./...` passes. `go test ./...` passes (exit 0).
- CI on main is green (run 29862257834).
- Toolchain present locally: Go 1.26.5, Node v26.5.0, Docker 29.6.1.

## Verdicts on the user's headline questions (evidence-backed)

- **Is the backend real?** YES. `server/server.go` is a genuine composition root
  wiring real command handlers → event log → projections → HTTP across all four
  components. 77k lines of Go, not scaffolding.
- **Is the frontend really connected?** YES. Pages import real API functions
  from `$lib/api` (multi-line imports; a naive one-line grep misses them and
  falsely reports "NO-API"). `web/src/lib/api.ts` (3754 lines) is a real REST
  client that throws on non-2xx via `errorOrStatus` rather than returning empty
  data. The `mock` hits under `web/src/routes/` are comments about the *wasm
  demo's* fetch bridge, not mocks in the served app.
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
