// SPDX-License-Identifier: AGPL-3.0-or-later

package leader_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/leader"
)

// TestClaimElectsExactlyOneLeaderPerEpoch races N replicas for the same epoch and
// proves exactly one wins, on the shared event log's unique-claim spine.
func TestClaimElectsExactlyOneLeaderPerEpoch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := eventlog.NewMemory()
	at := time.Unix(1_700_000_000, 0).UTC()
	epoch := leader.EpochFor(at, time.Minute)

	const replicas = 20
	var wg sync.WaitGroup
	wins := make(chan bool, replicas)
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func(holder int) {
			defer wg.Done()
			id := identity.Identity{Org: "demo", Workspace: "main", Actor: "replica"}
			won, err := leader.Claim(ctx, log, id, "flow_monitor", epoch, "holder-"+string(rune('a'+holder)), at)
			if err != nil {
				wins <- false
				return
			}
			wins <- won
		}(i)
	}
	wg.Wait()
	close(wins)

	winners := 0
	for won := range wins {
		if won {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("leaders elected = %d, want exactly 1 across %d replicas", winners, replicas)
	}

	// A second epoch is a fresh claim: the same replicas race again and elect one.
	epoch2 := leader.EpochFor(at.Add(time.Minute), time.Minute)
	winners2 := 0
	for i := 0; i < replicas; i++ {
		id := identity.Identity{Org: "demo", Workspace: "main", Actor: "replica"}
		won, err := leader.Claim(ctx, log, id, "flow_monitor", epoch2, "holder", at.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if won {
			winners2++
		}
	}
	if winners2 != 1 {
		t.Fatalf("second epoch leaders = %d, want 1 (a dead leader never blocks a later epoch)", winners2)
	}

	// Distinct roles do not contend for the same epoch.
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "replica"}
	won, err := leader.Claim(ctx, log, id, "model_drift", epoch, "holder", at)
	if err != nil {
		t.Fatal(err)
	}
	if !won {
		t.Fatal("a different role's claim for the same epoch must succeed independently")
	}
}
