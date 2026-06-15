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

Status: **in progress (Phase 2).**

Done — case lifecycle + queues (command→event→projection→API, durable & replayable):
- A case is opened by **ReviewRequested** (raised via the API now; escalation from a decision flow's
  `ManualReviewRequested` is the next slice) and evolves through assignment, status
  (`needs_review`/`in_progress`/`completed`), and notes. Every change emits an event and appends to
  the case's **audit log** — the detail view is reconstructed entirely from the stream.
- HTTP (under `/v1/`, X-Api-Key / session auth, org+workspace scoped):
  - `POST /v1/cases` — open `{company_name, case_type, sla_days, context?}` → `{case_id}`
  - `GET /v1/cases?status=&type=&assignee=` — the queue, filtered
  - `GET /v1/cases/{case_id}` — detail + notes + audit
  - `POST /v1/cases/{case_id}/assign|status|notes`
- Run it: `intraktible serve --modules=case-manager`.

Next (PLAN §4.2): the escalation hook (a decision flow emits `ManualReviewRequested` → a case is
opened, linked by `source_decision_id`); the dashboard UI (queue metrics + case detail) in `web/`;
SLA "days left" computation.
