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
	read func(ctx context.Context, fromSeq uint64) ([]Envelope, error)

	mu     sync.Mutex
	closed bool
	stop   chan struct{}
	wg     sync.WaitGroup

	// lastPub is the highest Seq published to the bus. After start it is only
	// touched by the single poller goroutine, so it needs no lock.
	lastPub uint64
}

// startDelivery begins polling read for events with Seq > head, publishing each
// to the bus. Seeding at head means only events appended from now on are
// delivered live (history is replayed via Read).
func startDelivery(read func(ctx context.Context, fromSeq uint64) ([]Envelope, error), poll time.Duration, head uint64) *delivery {
	if poll <= 0 {
		poll = DefaultPollInterval
	}
	d := &delivery{bus: newBus(), poll: poll, read: read, stop: make(chan struct{}), lastPub: head}
	d.wg.Add(1)
	go d.loop()
	return d
}

// subscribe hands out a live event channel (events appended after the call).
func (d *delivery) subscribe() (<-chan Envelope, func()) { return d.bus.subscribe() }

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
		case <-t.C:
			d.dispatch()
		}
	}
}

func (d *delivery) dispatch() {
	evs, err := d.read(context.Background(), d.lastPub+1)
	if err != nil {
		slog.Error("eventlog: poll failed", "err", err)
		return
	}
	for _, e := range evs {
		d.bus.publish(e)
		d.lastPub = e.Seq
	}
}
