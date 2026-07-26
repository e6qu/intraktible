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
