// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestDispatchBoundsBatchAndReArms proves the poll path reads in bounded passes
// (never the whole unread tail at once) and re-arms itself to drain a backlog that
// exceeds one batch. It asserts on the read progression — not the live bus, which
// is best-effort and may drop for a starved subscriber — since lastPub (and thus
// the next fromSeq) advances for every event regardless of delivery.
func TestDispatchBoundsBatchAndReArms(t *testing.T) {
	const total = dispatchBatch + 7 // more than one batch

	var mu sync.Mutex
	var maxLimit, calls int
	drained := make(chan struct{})
	var closed bool
	d := newDelivery(func(_ context.Context, fromSeq uint64, limit int) ([]Envelope, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if limit > maxLimit {
			maxLimit = limit
		}
		var out []Envelope
		for s := fromSeq; s <= total && len(out) < limit; s++ {
			out = append(out, Envelope{Seq: s})
		}
		if len(out) > 0 && out[len(out)-1].Seq == total && !closed {
			closed = true
			close(drained) // the final (partial) batch reached the tail
		}
		return out, nil
	}, time.Hour, 0) // long poll: only the re-arm (poke) can drive the second pass

	d.start()
	d.poke() // kick the first pass

	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("backlog not drained across passes")
	}
	_ = d.stopAndClose(func() error { return nil })

	mu.Lock()
	defer mu.Unlock()
	if maxLimit != dispatchBatch {
		t.Fatalf("poll read was not bounded: max limit %d, want %d", maxLimit, dispatchBatch)
	}
	if calls < 2 {
		t.Fatalf("expected the full first batch to re-arm a second read, got %d reads", calls)
	}
}

// TestStopCancelsInFlightRead proves Close unblocks a poll read that is parked
// (here, on the delivery context) rather than waiting it out.
func TestStopCancelsInFlightRead(t *testing.T) {
	entered := make(chan struct{})
	d := newDelivery(func(ctx context.Context, _ uint64, _ int) ([]Envelope, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done() // park until the delivery context is cancelled
		return nil, ctx.Err()
	}, time.Hour, 0)

	d.start()
	d.poke()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("poll read never started")
	}

	closed := make(chan error, 1)
	go func() { closed <- d.stopAndClose(func() error { return nil }) }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("stopAndClose: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stopAndClose blocked on an in-flight read instead of cancelling it")
	}
}
