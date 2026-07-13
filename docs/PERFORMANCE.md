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
