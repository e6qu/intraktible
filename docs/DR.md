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
- **NATS/JetStream log** (`--log=nats`): snapshot the events stream (JetStream backup)
  and the KV/consumer state per your NATS operator's runbook.
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

## DR drill (run this periodically)

1. Take an event-log backup on the primary.
2. Restore it into an isolated environment with the same encryption keys.
3. `intraktible serve` there; wait for `/readyz` to report `{"status":"ready"}` and
   confirm `applied == head`.
4. Spot-check: a known flow's live versions, a seeded decision's trace, the case
   queue counts — they must match the primary at the backup's `head`.
5. Record the RTO (time to `ready`) so it's a known quantity, not a hope.
