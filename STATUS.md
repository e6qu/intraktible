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

**Phase 1 — audit sweep, in progress.** No production code changed yet.
Findings so far are in `DO_NEXT.md`. Nothing fixed yet.

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
- **Production/multi-replica?** PARTLY. Real design work is there (single-writer
  projection checkpoint, Helm API tier + singleton scheduler with
  `strategy: Recreate`, HPA/PDB, probes). But there are genuine correctness
  gaps — see `DO_NEXT.md` PROD-1/3/4.
- **CI?** Comprehensive on paper and currently green; gap analysis not finished.
