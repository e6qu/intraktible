// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"testing"
	"time"
)

type fakeSweeper struct{ started chan time.Duration }

func (f *fakeSweeper) Run(_ context.Context, interval time.Duration) { f.started <- interval }

// TestStartTimedSweeps proves every configured scheduler starts independently
// on the shared cadence — the regression was the case-manager SLA scheduler
// starting only when the decision-engine's monitor scheduler existed, so
// --modules=case-manager silently never ran SLA sweeps.
func TestStartTimedSweeps(t *testing.T) {
	ctx := context.Background()

	sla := &fakeSweeper{started: make(chan time.Duration, 1)}
	mon := &fakeSweeper{started: make(chan time.Duration, 1)}
	if err := startTimedSweeps(ctx, "1h", []timedSweeper{sla, mon}); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]*fakeSweeper{"sla": sla, "monitor": mon} {
		select {
		case d := <-s.started:
			if d != time.Hour {
				t.Fatalf("%s sweeper started with interval %v, want 1h", name, d)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s sweeper never started", name)
		}
	}

	// A lone sweeper (the split-services shape) still starts.
	solo := &fakeSweeper{started: make(chan time.Duration, 1)}
	if err := startTimedSweeps(ctx, "1h", []timedSweeper{solo}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-solo.started:
	case <-time.After(5 * time.Second):
		t.Fatal("lone sweeper never started")
	}

	// Unset interval: sweeps stay off (nothing is spawned before the guard).
	off := &fakeSweeper{started: make(chan time.Duration, 1)}
	if err := startTimedSweeps(ctx, "", []timedSweeper{off}); err != nil {
		t.Fatal(err)
	}
	if len(off.started) != 0 {
		t.Fatal("sweeper must not start when the interval is unset")
	}

	// A malformed or non-positive interval fails loudly.
	for _, bad := range []string{"soon", "-1m", "0s"} {
		if err := startTimedSweeps(ctx, bad, nil); err == nil {
			t.Fatalf("interval %q should be rejected", bad)
		}
	}
}
