// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

// TestDispatchBoundsBatchAndReArms proves the poll path reads in bounded passes
// (never the whole unread tail at once) and re-arms itself to drain a backlog that
// exceeds one batch. It asserts on the read progression — not the live bus, which
// is best-effort and may drop for a starved subscriber — since lastPub (and thus
// the next fromSeq) advances for every event regardless of delivery.
//
// Run in a synctest bubble: synctest.Wait blocks until every goroutine is durably
// blocked, so we observe the backlog FULLY drained (the poll goroutine parked on its
// ticker again) without polling a channel against a wall-clock timeout. No shared-
// state mutex is needed either — after Wait the poll goroutine is blocked, so the
// counters it wrote are stable and race-free to read.
func TestDispatchBoundsBatchAndReArms(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const total = dispatchBatch + 7 // more than one batch

		var maxLimit, calls int
		var reachedTail bool
		d := newDelivery(func(_ context.Context, fromSeq uint64, limit int) ([]Envelope, error) {
			calls++
			if limit > maxLimit {
				maxLimit = limit
			}
			var out []Envelope
			for s := fromSeq; s <= total && len(out) < limit; s++ {
				out = append(out, Envelope{Seq: s})
			}
			if len(out) > 0 && out[len(out)-1].Seq == total {
				reachedTail = true // the final (partial) batch reached the tail
			}
			return out, nil
		}, time.Hour, 0) // long poll: only the re-arm (poke) can drive the second pass

		d.start()
		d.poke()        // kick the first pass
		synctest.Wait() // drain across passes until the poll goroutine is durably blocked
		_ = d.stopAndClose(func() error { return nil })

		if !reachedTail {
			t.Fatal("backlog not drained across passes")
		}
		if maxLimit != dispatchBatch {
			t.Fatalf("poll read was not bounded: max limit %d, want %d", maxLimit, dispatchBatch)
		}
		if calls < 2 {
			t.Fatalf("expected the full first batch to re-arm a second read, got %d reads", calls)
		}
	})
}

// TestStopCancelsInFlightRead proves Close unblocks a poll read that is parked
// (here, on the delivery context) rather than waiting it out.
//
// In a synctest bubble this is fully deterministic with no timeouts and no
// rendezvous channel: synctest.Wait returns once the poll read is durably blocked on
// ctx.Done (so `entered` is set), and a regression where stopAndClose waits out the
// parked read would deadlock the bubble — synctest fails it immediately with a
// goroutine dump rather than hanging until a wall-clock timeout.
func TestStopCancelsInFlightRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := false
		d := newDelivery(func(ctx context.Context, _ uint64, _ int) ([]Envelope, error) {
			entered = true
			<-ctx.Done() // park until the delivery context is cancelled
			return nil, ctx.Err()
		}, time.Hour, 0)

		d.start()
		d.poke()
		synctest.Wait() // the poll read has run and is now durably blocked on ctx.Done
		if !entered {
			t.Fatal("poll read never started")
		}

		// stopAndClose must cancel that parked read, not wait it out.
		if err := d.stopAndClose(func() error { return nil }); err != nil {
			t.Fatalf("stopAndClose: %v", err)
		}
	})
}
