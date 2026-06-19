// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// delivery turns a pollable, durable backend (SQLite, Postgres) into a live
// in-process bus: backends without native pub/sub share one ordered log, and a
// single poller reads newly-committed rows (this process's and others') and
// publishes them to subscribers. Read/Head stay immediately consistent; live
// delivery is eventual, bounded by the poll interval (just projection lag).
type delivery struct {
	bus  *bus
	poll time.Duration
	read func(ctx context.Context, fromSeq uint64, limit int) ([]Envelope, error)

	// ctx is cancelled on close so an in-flight backend read on the poll path
	// unblocks promptly rather than holding up shutdown until it finishes.
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool
	stop   chan struct{}
	wg     sync.WaitGroup
	// wake triggers an immediate dispatch between ticks — a backend with a
	// push signal (e.g. Postgres LISTEN/NOTIFY) pokes it so delivery does not
	// wait out the poll interval. Buffered to 1: a poke while a dispatch is in
	// flight coalesces into one follow-up pass.
	wake chan struct{}

	// lastPub is the highest Seq published to the bus. After start it is only
	// touched by the single poller goroutine, so it needs no lock.
	lastPub uint64
}

// newDelivery constructs a poller seeded at head (only events appended from now
// on are delivered live; history is replayed via Read). It does NOT start the
// goroutine — the caller must store the *delivery on its log and then call start,
// so the poller never reads a half-initialised log (the goroutine calls back into
// the log's Read, which dereferences the very field being assigned).
func newDelivery(read func(ctx context.Context, fromSeq uint64, limit int) ([]Envelope, error), poll time.Duration, head uint64) *delivery {
	if poll <= 0 {
		poll = DefaultPollInterval
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &delivery{
		bus: newBus(), poll: poll, read: read,
		ctx: ctx, cancel: cancel,
		stop: make(chan struct{}), wake: make(chan struct{}, 1), lastPub: head,
	}
}

// start launches the polling goroutine. Call it only after the *delivery is
// published on the owning log (see newDelivery).
func (d *delivery) start() {
	d.wg.Add(1)
	go d.loop()
}

// subscribe hands out a live event channel (events appended after the call).
func (d *delivery) subscribe() (<-chan Envelope, func()) { return d.bus.subscribe() }

// poke requests an immediate dispatch (non-blocking). A backend's push signal
// calls it; missing or coalescing pokes is harmless — the poll loop is the
// correctness floor, poke is only a latency optimization.
func (d *delivery) poke() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// isClosed reports whether the log has been closed.
func (d *delivery) isClosed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closed
}

// stopAndClose stops the poller, drains subscriptions, then closes the backend.
// It is idempotent.
func (d *delivery) stopAndClose(closeBackend func() error) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	close(d.stop)
	d.cancel() // unblock any in-flight poll read
	d.mu.Unlock()

	d.wg.Wait()
	d.bus.closeAll()
	return closeBackend()
}

func (d *delivery) loop() {
	defer d.wg.Done()
	t := time.NewTicker(d.poll)
	defer t.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-d.wake:
			d.dispatch()
		case <-t.C:
			d.dispatch()
		}
	}
}

// dispatchBatch caps how many events one poll pass pulls into memory. A larger
// backlog (e.g. a node that was offline) drains across successive passes, which
// are re-armed immediately, instead of loading the whole unread tail at once.
const dispatchBatch = 1024

func (d *delivery) dispatch() {
	evs, err := d.read(d.ctx, d.lastPub+1, dispatchBatch)
	if err != nil {
		if d.ctx.Err() != nil {
			return // shutting down: the cancelled read is expected, not a failure
		}
		slog.Error("eventlog: poll failed", "err", err)
		return
	}
	for _, e := range evs {
		d.bus.publish(e)
		d.lastPub = e.Seq
	}
	if len(evs) == dispatchBatch {
		// A full batch means more may be waiting; re-arm without waiting out the
		// poll interval (poke coalesces, so this never piles up).
		d.poke()
	}
}
