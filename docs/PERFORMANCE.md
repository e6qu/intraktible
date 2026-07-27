<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# Decision-throughput baseline

This records a reproducible measurement of the decision engine's core throughput, so
"how fast can it decide, and does it scale across cores" has an answer backed by a
benchmark rather than a claim. It is a starting baseline, not a production benchmark —
read the caveats.

## Reproduce

```
make bench
```

Runs `BenchmarkDecide` (serial) and `BenchmarkDecideParallel` (concurrent, at `-cpu
1,2,4,8`) in `decision-engine/decide_bench_test.go`. Each iteration is one full decide:
validate the request, execute a representative flow (an assignment computing a score, a
split, and branch assignments — real expression evaluation, not a no-op), and construct
the decision's event stream (started → node-evaluated… → completed).

## What was measured

A single run, `-benchtime 2s`:

| Benchmark | ns/op | decisions/sec | B/op | allocs/op |
|---|---|---|---|---|
| `BenchmarkDecide` (serial) | 52,980 | ~18,900 | 56,892 | 476 |
| `BenchmarkDecideParallel-1` | 54,283 | ~18,400 | 59,352 | 475 |
| `BenchmarkDecideParallel-2` | 29,613 | ~33,800 | 57,056 | 476 |
| `BenchmarkDecideParallel-4` | 17,274 | ~57,900 | 57,561 | 476 |
| `BenchmarkDecideParallel-8` | 13,779 | ~72,600 | 59,391 | 474 |

Environment: `darwin/arm64`, Apple M4 Pro, Go's default settings.

## Reading it

- **Single-threaded throughput is ~18,900 decisions/sec** for this flow, at ~476
  allocations and ~57 KB per decision.
- **It scales across cores**: ~1.8× at 2 cores, ~3.1× at 4, ~3.9× at 8. The tail-off past
  4 cores is contention on the shared in-memory event log and store (a single mutex each);
  under real concurrency a per-decision cost of ~14 µs at 8 cores gives ~72,600
  decisions/sec on this machine.
- **No data race**: the concurrent path passes `go test -race`.

## The Postgres event log: what serialized appends cost

The numbers above measure the decide path against an **in-memory** log. The networked
log is a different regime, and one with a deliberate ceiling in it.

`PostgresLog.Append` serializes every append on an advisory lock held to COMMIT, so
that commit order equals seq order. That is not a tuning choice: `BIGSERIAL` assigns a
seq at INSERT but a row becomes visible at COMMIT, so concurrent appends could be
assigned 4 and 5 and commit as 5 then 4 — and the delivery poller, which advances to
the highest seq it has read, would never deliver seq 4 to any replica. (Reproduced by
`TestPostgresLogConcurrentAppendsPreserveCommitOrder`, which without the lock loses
events on every run.)

```
INTRAKTIBLE_TEST_POSTGRES='postgres://…' go test ./platform/eventlog/ \
  -run xxx -bench BenchmarkPostgresAppend -benchtime 3s -cpu 8 -count 5
```

Median of 5 runs, PostgreSQL 16, **Linux** (both the benchmark and the server in
containers on one Docker network, so neither the client hop nor fsync crosses a VM
boundary), against a build with and without the lock:

| | with the lock | without | cost |
|---|---|---|---|
| serial (one appender) | ~1.03 ms/op · ~967 appends/sec | ~0.76 ms/op · ~1,312/sec | ~1.36× |
| parallel, 8 appenders | ~0.56 ms/op · ~1,795 appends/sec | ~0.26 ms/op · ~3,802/sec | ~2.12× |

**Reading it.** The lock costs about a third of single-appender throughput — it is an
extra round-trip on every append, so it is not free even uncontended — and about 2.1×
under eight concurrent appenders.

The more useful number is the one that is easy to assume away: **appends still scale
with concurrency under the lock**, ~1.9× from one appender to eight (967 → 1,795/sec).
Serializing the *critical section* does not serialize the *fsyncs*: PostgreSQL group
commit batches the flushes of transactions queued behind the lock, so added concurrency
still buys throughput. The ceiling is lower than it would be without the lock; it is not
flat.

> An earlier revision of this page reported these as measured on Docker for macOS and
> concluded throughput was flat in the number of appenders. That was an artifact of the
> macOS VM's fsync path, which was slow enough to dominate everything else: it put
> serial and parallel within noise of each other (~565/sec vs ~650/sec) and overstated
> the concurrent cost as ~2.6×. The Linux figures above supersede it. If you re-measure
> on macOS you will reproduce the old numbers — they are a property of that storage
> path, not of this code.

Two things still keep ~1,795/sec from being the system's write ceiling. It is an
**event** rate, not a decision rate, and one decision emits several events (started, one
per node, completed). And a managed PostgreSQL on provisioned IOPS is a different
machine again. Treat the ratios as durable and the absolute numbers as this setup's.

**The alternative, and why it is not shipped.** Appends could stay concurrent if the
poller instead withheld any row whose transaction might still be in flight, gating its
watermark on the visibility horizon (`pg_snapshot_xmin`). That would recover the ~2.1×.
It is not here because it turns on 32-bit `xid` versus 64-bit `xid8` comparison and is
therefore only correct if it handles transaction-id wraparound — a condition that cannot
be exercised without on the order of 2^31 transactions, so shipping it would mean
shipping a correctness argument no test in this repo can check. The lock is the design
that is provably right with the tools available. If the ceiling starts to bind, that is
the direction, and `BenchmarkPostgresAppend` is the measurement to beat.

## Caveats — what this is NOT

- **In-memory log and store.** This isolates the decision core from disk. A durable WAL
  fsyncs on append and a Postgres store adds network round-trips per decision — both add
  latency this number does not include. There are no durable-backend or Postgres numbers
  here yet.
- **One flow shape.** Throughput depends on the flow: a Connect node (external fetch), an
  AI node (a provider call), or a Predict node (a gradient-boosted model) each cost far
  more than the arithmetic-and-branch flow measured here. This is a floor for trivial
  logic, not a figure for a heavy flow.
- **No projection under load.** The benchmark exercises the synchronous decide (which
  appends events); it does not measure the async projection runtime keeping read models
  current under a sustained write rate, which is single-node today.
- **Failure-injection and soak now covered; no multi-hour endurance run.** The decide
  boundary's fail-loud guarantees are tested (`decision-engine/decide_resilience_test.go`):
  a one-nanosecond evaluation budget makes a decision fail quickly instead of hanging, and
  a failing Connect, AI, or Predict node fails the decision loudly rather than completing
  with that step's data silently missing. A **dying store mid-decide** is now injected too
  (`decision-engine/decide_soak_test.go`): a decide whose projection store returns errors
  fails loud rather than completing against a store that dropped its reads (with a positive
  control and a heal-recovery check). A **soak test** in the same file drives 1,000
  concurrent decides over the segmented durable WAL with a small segment cap — so segments
  rotate and gzip-archive continuously under load — then asserts no drift: the log reads
  back gap-free (seq 1..head, no holes or duplicates), Head matches the record count, and
  compaction actually engaged. What this is NOT: a multi-hour endurance run measuring
  resident-memory flatness over time — the soak asserts correctness under sustained load,
  not a days-long leak profile.
- **One machine, one run.** Absolute numbers are machine- and run-specific; treat the
  ratios (core scaling) as more durable than the absolute nanoseconds.
