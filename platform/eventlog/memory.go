// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import (
	"context"
	"fmt"
	"sync"
)

// MemoryLog is a purely in-RAM, append-only event log: the backend for the
// js/wasm deployment target (a browser has no filesystem or database) and for
// tests. It honors the full Log contract — the same envelope stamping and
// validation as the WAL, the same non-blocking subscriber bus, the same Read
// snapshot semantics — but nothing is durable: the process's memory IS the log.
// For persistence the wasm host Export()s the log (e.g. to localStorage) and
// replays it on the next boot via NewMemoryFrom.
type MemoryLog struct {
	mu      sync.Mutex
	events  []Envelope
	bus     *bus
	closed  bool
	claimed map[string]bool
}

// NewMemory returns an empty in-memory log.
func NewMemory() *MemoryLog {
	return &MemoryLog{bus: newBus(), claimed: map[string]bool{}}
}

// NewMemoryFrom rebuilds a log from a previously Export()ed history, restoring
// head/seq and the Unique claims. The history must be complete and contiguous
// (seq 1..n in order) — a gap or disorder means the caller lost or reordered
// events, so it fails loudly rather than replay a corrupt history.
func NewMemoryFrom(events []Envelope) (*MemoryLog, error) {
	l := NewMemory()
	var want uint64
	for i, e := range events {
		want++
		if e.Seq != want {
			return nil, fmt.Errorf("eventlog: memory history seq %d at index %d, want %d (gap or disorder)", e.Seq, i, want)
		}
		if e.Unique != "" {
			l.claimed[e.Unique] = true
		}
	}
	l.events = append([]Envelope(nil), events...)
	return l, nil
}

// Append assigns the next Seq, stores the event, then publishes. Caller-supplied
// Seq is ignored; tenancy and Time must be set by the (imperative-shell) caller.
func (l *MemoryLog) Append(_ context.Context, e Envelope) (Envelope, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return Envelope{}, ErrClosed
	}
	e, err := stampForAppend(e, l.claimed, uint64(len(l.events))+1)
	if err != nil {
		return Envelope{}, err
	}
	l.events = append(l.events, e)
	if e.Unique != "" {
		l.claimed[e.Unique] = true
	}
	l.bus.publish(e)
	return e, nil
}

// Read returns a copy of all events with Seq >= fromSeq, in order — a snapshot,
// unaffected by later appends.
func (l *MemoryLog) Read(_ context.Context, fromSeq uint64) ([]Envelope, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, ErrClosed
	}
	if fromSeq == 0 {
		fromSeq = 1
	}
	if fromSeq > uint64(len(l.events)) {
		return nil, nil
	}
	return append([]Envelope(nil), l.events[fromSeq-1:]...), nil
}

// Subscribe returns events appended after the call (in-process bus).
func (l *MemoryLog) Subscribe() (<-chan Envelope, func()) { return l.bus.subscribe() }

// Head returns the highest assigned Seq (0 when empty).
func (l *MemoryLog) Head() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return uint64(len(l.events))
}

// Export returns a copy of the entire history (seq 1..Head), the state the
// host persists and later feeds back to NewMemoryFrom.
func (l *MemoryLog) Export() []Envelope {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Envelope(nil), l.events...)
}

// Close closes the log and all subscriptions.
func (l *MemoryLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	l.bus.closeAll()
	return nil
}
