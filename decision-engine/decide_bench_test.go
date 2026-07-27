// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// benchSetup publishes a representative decision flow (an assignment, a split, and
// branch assignments — real expression evaluation, not a no-op) and returns a handler
// ready to decide against it. The event log is in-memory so the benchmark measures the
// decision core (validation, execution, expression VMs, event construction) rather than
// disk fsync latency, which varies by machine; durable persistence adds per-decision I/O
// on top of these numbers.
func benchSetup(b *testing.B) (context.Context, *command.DecideHandler, identity.Identity) {
	b.Helper()
	return benchSetupOn(b, eventlog.NewMemory())
}

// benchSetupOn is benchSetup against a caller-chosen log, so the same flow and input
// can be measured on each durable backend.
func benchSetupOn(b *testing.B, log eventlog.Log) (context.Context, *command.DecideHandler, identity.Identity) {
	b.Helper()
	ctx := context.Background()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFlow(b, ctx, log, st, id, "risk", "Risk", flowtest.DecisionGraph())
	return ctx, command.NewDecideHandler(log, st), id
}

// benchInput is a realistic decide payload for DecisionGraph (score = fico + bonus).
func benchInput() map[string]any { return map[string]any{"fico": 720, "bonus": 15} }

// BenchmarkDecide measures single-threaded decision throughput: one full decide
// (validate → execute the graph → record the decision event stream) per iteration.
func BenchmarkDecide(b *testing.B) {
	ctx, dh, id := benchSetup(b)
	// Warm up so the first iteration doesn't pay the expression-VM compile.
	if _, err := dh.Decide(ctx, id, "risk", "sandbox", benchInput(), command.EntityRef{}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := dh.Decide(ctx, id, "risk", "sandbox", benchInput(), command.EntityRef{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecideParallel measures concurrent decision throughput across GOMAXPROCS
// goroutines sharing one handler, log, and store — the shape of the synchronous decide
// path under concurrent /decide requests. Run with -cpu 1,2,4,8 to see how throughput
// scales with cores.
func BenchmarkDecideParallel(b *testing.B) {
	ctx, dh, id := benchSetup(b)
	if _, err := dh.Decide(ctx, id, "risk", "sandbox", benchInput(), command.EntityRef{}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := dh.Decide(ctx, id, "risk", "sandbox", benchInput(), command.EntityRef{}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// The in-memory benchmarks above deliberately exclude disk, and the setup comment has
// long said durable persistence "adds per-decision I/O on top of these numbers"
// without saying how much. These measure it.
//
// A decide is not one append: it records started, one node-evaluated per node, and
// completed. So the durable cost is several fsync-bounded appends per decision, and
// the ratio against the in-memory figure is the honest statement of what the event
// log costs the synchronous decide path.

// BenchmarkDecideFileWAL measures the default single-replica durable log.
func BenchmarkDecideFileWAL(b *testing.B) {
	log, err := eventlog.OpenWAL(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = log.Close() })

	ctx, dh, id := benchSetupOn(b, log)
	if _, err := dh.Decide(ctx, id, "risk", "sandbox", benchInput(), command.EntityRef{}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := dh.Decide(ctx, id, "risk", "sandbox", benchInput(), command.EntityRef{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecideSQLiteLog measures the log the split-services profile shares across
// processes.
func BenchmarkDecideSQLiteLog(b *testing.B) {
	log, err := eventlog.OpenSQLiteLog(b.TempDir(), time.Hour) // long poll: measure appends, not delivery
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = log.Close() })

	ctx, dh, id := benchSetupOn(b, log)
	if _, err := dh.Decide(ctx, id, "risk", "sandbox", benchInput(), command.EntityRef{}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := dh.Decide(ctx, id, "risk", "sandbox", benchInput(), command.EntityRef{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecidePostgresLog measures the networked HA log, whose appends serialize
// on an advisory lock so commit order matches seq order. This is the number that
// bounds a multi-replica deployment's decision throughput.
func BenchmarkDecidePostgresLog(b *testing.B) {
	dsn := os.Getenv("INTRAKTIBLE_TEST_POSTGRES")
	if dsn == "" {
		b.Skip("set INTRAKTIBLE_TEST_POSTGRES (a pgx DSN) to benchmark the networked log")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS events`); err != nil {
		b.Fatal(err)
	}
	pool.Close()

	log, err := eventlog.OpenPostgresLog(ctx, dsn, time.Hour)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = log.Close() })

	_, dh, id := benchSetupOn(b, log)
	if _, err := dh.Decide(ctx, id, "risk", "sandbox", benchInput(), command.EntityRef{}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := dh.Decide(ctx, id, "risk", "sandbox", benchInput(), command.EntityRef{}); err != nil {
			b.Fatal(err)
		}
	}
}
