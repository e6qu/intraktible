<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# WHAT_WE_DID — completed work, newest last

Append one line per completed item, with the evidence that proves it done
(a passing gate, a test name, a file:line). This is the history; the live queue
is `DO_NEXT.md`.

---

## Session 1 — 2026-07-27 — production-readiness audit

Branch `hardening/production-readiness-audit`, off `292d882`.

### Setup
- Created the continuity convention (`STATUS.md`, `WHAT_WE_DID.md`,
  `DO_NEXT.md`, plus the existing `PLAN.md` as plan-of-record) and documented it
  in `AGENTS.md`.
- `CLAUDE.md` is now a symlink to `AGENTS.md`, so both entry points are one file.

### Audit findings established (no production code changed yet)
- Verified the backend is real, not scaffolding: `server/server.go` is a genuine
  composition root wiring command handlers → event log → projections → HTTP for
  all four components.
- Verified the frontend genuinely talks to the real `/v1` API via
  `web/src/lib/api.ts`; no mock data path in the served app.
- Verified `go build ./...` and `go test ./...` pass at the base commit, and CI
  on `main` is green (run 29862257834).
- Found and queued PROD-1..4 and BROKEN-1 — see `DO_NEXT.md`.
