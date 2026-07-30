<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->

# Case Manager

A component of **intraktible** (see [../PLAN.md](../PLAN.md) §4.2). New here? Start at [../AGENTS.md](../AGENTS.md).

Layout (functional core / imperative shell):
```
domain/      # pure command validation (status enum, request/assign/status/note)
events/      # event payloads (ReviewRequested, CaseAssigned, CaseStatusChanged, CaseNoteAdded)
command/     # validate (pure) -> emit events (lifecycle commands verify the case exists)
cases/       # events -> JSONB read model (queue/detail + an audit log built from events)
service/     # HTTP handlers + wiring (imperative shell)
```

Status: **enterprise case operations complete (`PLAN.md` §8b.4).**

Immutable case-type versions define typed/required fields, dynamic transitions
and roles, dispositions/reasons, priorities, business calendars, evidence
requirements, PII readers, and role layouts. Governed cases validate context and
pin their exact version/deadline; historical cases remain explicit compatibility
version `0`. Layout-declared editable fields have a role-authorized, typed,
concurrency-safe event path and replay into the same context projection. Durable
ordered queues and reviewer profiles route by attributes,
skills, capacity, jurisdiction, priority, age, and conflicts. Permanent claims
protect initial routing, takeover/rebalance, escalation, and terminal actions
across replicas. Scheduler reconciliation recovers a crash after a breach was
recorded but before its queue move.

Done — complete case operations vertical (command→event→projection→API/UI,
durable and replayable):
- A case is opened either via the API (**ReviewRequested**) or by a **decision flow** — a
  `manual_review` node makes the engine emit `decision.manual_review_requested`, which the `cases`
  projector consumes to open a case linked by `source_decision_id` (the components talk only through
  the event log). It then evolves through assignment, status
  (`needs_review`/`in_progress`/`completed`), and notes. Every change emits an event and appends to
  the case's **audit log** — the detail view is reconstructed entirely from the stream.
- **SLA tracking**: each case carries `days_left` and an `sla_state` of `on_track` / `due_soon`
  (within a day) / `overdue`, computed from `created_at + sla_days` against the clock **at the read
  boundary** (the `domain.SLAState`/`DaysLeft` pure functions) so the stored projection stays
  clock-free and replay-stable. A queue **summary** rolls these up (totals by status, unassigned,
  due-soon, overdue) over the same filtered set as the list.
- **SLA-breach events** (pushed, not only derived): an **SLA sweep** (`POST /v1/cases/sla-sweep`, for
  a scheduler/cron to call) finds open cases past their deadline as of now and emits a
  `CaseSLABreached` event for each — an effect computed against the clock and then *recorded* (so
  replay stays stable), idempotent (a breached case is skipped). The projection marks `sla_breached`
  and audits it; the scheduler reconciles configured queue escalation and bounded external webhook
  attempts on every tick, including after restart. Delivery attempts, dead-letter outcome, and
  explicit retry rounds are projected.
- **Operational workbench:** text and structured search, personal saved views, opaque duplicate
  candidates, deterministic queue rebalance, and bounded idempotent server-side bulk manifests for
  assignment, status, priority, and disposition. Analytics cover workload/capacity, first action,
  resolution, backlog age, SLA/routing state, and QA.
- **Evidence and review truth:** typed links, immutable attachment hash/metadata, required-evidence
  gates, reasoned dispositions, deterministic QA sampling, independent second review,
  agreement/disagreement/override, and reviewer feedback. Only agreement/override-backed outcomes
  enter the validated-outcome feed.
- **Privacy/lifecycle:** exact-version PII masking, lawful basis, statutory retention, legal hold,
  erasure, audit export, and audited attachment access. Binary bytes remain in an approved external
  artifact system: ordinary reads redact the storage pointer; a purpose-bound operator command
  records access before returning it.
- HTTP (under `/v1/`, X-Api-Key / session auth, org+workspace scoped):
  - `/v1/case-types`, `/v1/case-queues`, `/v1/case-reviewers` — immutable type publication and active
    routing configuration
  - `POST /v1/cases` — open governed work; `GET /v1/cases?...` — search/filter queue
  - `GET /v1/cases/summary?status=&type=&assignee=` — the queue roll-up
  - `GET /v1/cases/{case_id}` — detail + notes + audit
  - case actions: route/assign/status/priority/fields/disposition/notes/evidence/attachments/QA/webhook retry
  - `/v1/case-views`, `/v1/cases/bulk`, `/v1/cases/duplicates`, `/v1/cases/rebalance`,
    `/v1/cases/analytics`, `/v1/cases/export`, `/v1/case-validated-outcomes`
- **Dashboard UI** (`web/src/routes/cases`): governed type/queue/reviewer administration; queue
  search, saved views, duplicates, four bulk operations, analytics and export; role-ordered detail
  with routing, lifecycle, typed field editing, evidence/attachment access, disposition, independent QA, webhook history,
  notes, immutable activity, and discussion.
- Run it: `intraktible serve --modules=case-manager` (UI dev: `make dev`).

The Go and TypeScript SDKs and OpenAPI document expose the same contracts. The seeded hosted demo
uses these actual handlers through WebAssembly; there is no case-management mock.
