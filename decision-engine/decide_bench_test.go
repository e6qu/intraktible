// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"testing"

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
	ctx := context.Background()
	log := eventlog.NewMemory()
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
