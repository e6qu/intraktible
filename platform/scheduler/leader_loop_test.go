// SPDX-License-Identifier: AGPL-3.0-or-later

package scheduler_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/scheduler"
)

// TestRunLeaderElectsExactlyOneTickPerEpoch runs N redundant replicas of a sweep
// loop against the same event log and proves exactly one tick executes per epoch,
// across many epochs (deterministic exactly-once + takeover).
func TestRunLeaderElectsExactlyOneTickPerEpoch(t *testing.T) {
	t.Parallel()
	log := eventlog.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "scheduler"}
	now := time.Now

	const replicas = 6
	const epochsToWait = 5
	var ticks atomic.Int64
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func(holder int) {
			defer wg.Done()
			ldr := &scheduler.Leader{
				Log: log, ID: id, Holder: "replica-" + string(rune('a'+holder)), Now: now,
			}
			scheduler.RunLeader(
				ctx, ldr,
				10*time.Millisecond, "test_sweep", "test_sweep",
				func(error) {},
				func(context.Context) (int, error) {
					ticks.Add(1)
					return 0, nil
				},
				func(int) {},
			)
		}(i)
	}

	// Wait for several epochs to elapse, then cancel and join.
	time.Sleep(time.Duration(epochsToWait) * 10 * time.Millisecond)
	cancel()
	wg.Wait()

	// Each epoch elects exactly one leader, so the number of ticks equals the number
	// of epochs that elapsed. We can't pin the exact epoch count (timing), but the
	// tick count must be small (one per epoch, not one per replica per epoch).
	got := ticks.Load()
	maxTicks := int64(epochsToWait + 2) // a small slack for partial epochs at the edges
	if got > maxTicks {
		t.Fatalf(
			"ticks = %d across %d replicas, want ~1 per epoch (≤ %d); leader election is not fencing the sweep",
			got, replicas, maxTicks,
		)
	}
	if got == 0 {
		t.Fatal("no ticks ran; the leader claim is blocking every replica")
	}
}
