// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import "sync"

// bus is an in-process fan-out for appended events (monolith deployment).
// A distributed backbone (NATS/Kafka) would replace this behind Log.Subscribe.
type bus struct {
	mu   sync.Mutex
	next int
	subs map[int]chan Envelope
}

func newBus() *bus { return &bus{subs: make(map[int]chan Envelope)} }

func (b *bus) subscribe() (<-chan Envelope, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	ch := make(chan Envelope, 256)
	b.subs[id] = ch
	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
	return ch, cancel
}

// publish delivers e to all subscribers. It never blocks the appender: a
// subscriber whose buffer is full is skipped (it must rebuild from the log via
// Read on its own checkpoint), so a slow consumer can't stall the write path.
func (b *bus) publish(e Envelope) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func (b *bus) closeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, ch := range b.subs {
		delete(b.subs, id)
		close(ch)
	}
}
