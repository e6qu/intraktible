<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# Backup & disaster recovery

intraktible is event-sourced: the **event log is the single system of record**, and
every projection (read model) is a pure function of it. That shapes the whole DR
story — back up the log, and everything else is rebuildable.

## What to back up

| Data | Backend | Rebuildable? | Backup |
| --- | --- | --- | --- |
| **Event log** (source of truth) | `--log=postgres` / `nats` / `file`(WAL) / `sqlite` | **No — back this up** | see below |
| **Projection store** (read models) | `--store=postgres` / `sqlite` / memory | **Yes** — replayed from the log | optional (speeds recovery) |
| **Encryption keys** | `INTRAKTIBLE_ENCRYPTION_KEY`, `…_KEYS_PREVIOUS`, `INTRAKTIBLE_CONNECTOR_SECRET_KEY` | **No — back these up** | your secret manager |

> Losing the encryption key makes everything sealed under it permanently unreadable —
> the erasure feature relies on exactly this. Back the keys up in your secret manager
> with the same rigor as the database.

## Backing up the event log

- **Postgres log** (`--log=postgres`): `pg_dump` (or continuous WAL archiving / a
  managed PITR snapshot) of the log database. This is the primary artifact.
- **NATS/JetStream log** (`--log=nats`): snapshot the `INTRAKTIBLE_EVENTS` stream
  per your NATS operator's runbook. The permanent optimistic-claim index is the
  retained event's injectively encoded subject in that same stream; live
  consumers are ephemeral, so there is no separate KV or consumer state needed
  for restore.
- **File WAL** (`--log=file`): copy `<data-dir>/events.log` while the process is
  quiesced (or filesystem-snapshot it).
- **SQLite log** (`--log=sqlite`): back up `<data-dir>/events-log.db` (use the SQLite
  online backup API or snapshot during low write).

The projection store is optional to back up — restoring it only saves the replay
time on recovery.

## Restore

1. Restore the event-log backend from its backup (and the encryption keys into the
   environment).
2. Start the service. On boot the projection runtime **rebuilds the read models from
   the log**; `/readyz` stays 503 until it has caught up, then flips to 200.
3. That's the whole recovery — no separate "restore read models" step is required. If
   you also restored the projection store backup, the rebuild is incremental (from the
   store's last applied seq) rather than from seq 0.

## Point-in-time recovery / audit reconstruction

Because state is `fold(events)`, you can reconstruct the exact read model as of any
past point without a time-series backup:

```sh
# Inspect the log.
intraktible log --data-dir=/data | tail

# Rebuild projections as of a specific sequence into a scratch store, then inspect.
intraktible replay --data-dir=/data --as-of=<seq>
```

`replay --as-of` folds only events up to `<seq>`, giving the precise historical state
(a suspended decision, a case's status, a flow's live version) at that moment — useful
for audit reconstruction and for verifying a backup.

## Portable backup & restore (`intraktible backup` / `intraktible restore`)

Beyond backend-native dumps, the CLI ships a portable, backend-agnostic pair:

```sh
# Stream the whole log to a newline-delimited JSON file (the recovery artifact).
intraktible backup --data-dir=/data --out=backup-$(date +%F).jsonl

# Restore it into a FRESH data directory (which must not already have a log).
intraktible restore --data-dir=/restore --in=backup-2026-08-01.jsonl

# Verify the restore replays cleanly.
intraktible replay --data-dir=/restore
```

The restored log is byte-identical to the source (same seq numbers, payloads, and
optimistic-concurrency claims), so it can be served immediately. The contract is
proved by `TestBackupRestoreReplayRoundTrip` and `TestBackupRestoreCLIRoundTrip` in
`cmd/intraktible/backup_test.go`.

## Service-level targets

- **RPO: zero.** The event log fsyncs every append (WAL/SQLite/Postgres) or awaits the
  JetStream publish-ack before the write is acknowledged, so a committed event is
  never lost on a process or node failure.
- **RTO: replay time.** Recovery time is the time to replay the log into a fresh
  projection store (plus process start). `GET /capacity` reports live
  `applied`/`head`/lag so the operator can watch catch-up complete; `/readyz` stays
  503 until the replica is current. Measure RTO with the DR drill below and record it.

## Consistency model (published)

- **Total order, per log.** Every event carries a single monotonically increasing
  `seq`. The append path serializes order; the consistency ceiling today is one
  global ordered log per deployment (the event-spine partition design in PLAN §8b.8
  is deliberately conservative — canonical order is retained where replay safety
  requires it).
- **Per-tenant reads, replay, and claims.** Reads and `replay` are scoped by
  `(org, workspace)` key prefix; cross-tenant isolation is enforced there and
  covered by `TestCrossTenantIsolation`. Optimistic-concurrency claims
  (`Envelope.Unique`) fence exactly-once outcomes per tenant across replicas.
- **Eventual consistency for read models.** Projections are rebuilt views over the
  log. A single-writer checkpoint (locked row on durable stores) makes multi-replica
  apply exactly-once; `/readyz` gates a catching-up replica, and the optional
  `INTRAKTIBLE_MAX_PROJECTION_LAG` backpressure sheds reads past a lag bound.
- **Leader-elected background work.** Scheduler sweeps (monitor, drift, deploy,
  retention, SLA, governance, modeling) elect one leader per epoch via event-log
  claims, so redundant replicas tick exactly-once per epoch (`platform/leader`).

## DR drill (run this periodically)

1. Take an event-log backup on the primary (`intraktible backup` or a native dump).
2. Restore it into an isolated environment with the same encryption keys.
3. `intraktible serve` there; wait for `/readyz` to report `{"status":"ready"}` and
   confirm `applied == head` (also visible on `/capacity`).
4. Spot-check: a known flow's live versions, a seeded decision's trace, the case
   queue counts — they must match the primary at the backup's `head`.
5. Record the RTO (time to `ready`) so it's a known quantity, not a hope.
