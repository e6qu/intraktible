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

Status: **done (Phase 2).**

Done — case lifecycle + queues + flow escalation + SLA tracking (command→event→projection→API, durable & replayable):
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
- HTTP (under `/v1/`, X-Api-Key / session auth, org+workspace scoped):
  - `POST /v1/cases` — open `{company_name, case_type, sla_days, context?}` → `{case_id}`
  - `GET /v1/cases?status=&type=&assignee=` — the queue, filtered (each case includes `days_left`/`sla_state`)
  - `GET /v1/cases/summary?status=&type=&assignee=` — the queue roll-up
  - `GET /v1/cases/{case_id}` — detail + notes + audit
  - `POST /v1/cases/{case_id}/assign|status|notes`
- **Dashboard UI** (`web/src/routes/cases`): a queue (status filter + open-case form, a summary
  banner, and per-row days-left) and a case-detail view (fields incl. days-left, notes, **audit
  log**, and assign / set-status / add-note actions).
- Run it: `intraktible serve --modules=case-manager` (UI dev: `make dev`).

Deferred (see [../BUGS.md](../BUGS.md)): no SLA-breach events/alerts — overdue is derived on read,
not pushed (D12); no rich/schema-aware context view in case detail (D13).
