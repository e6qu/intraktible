// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/nats-io/nats.go"
)

const (
	natsStream  = "INTRAKTIBLE_EVENTS"
	natsSubject = "intraktible.events"
)

// NATSLog is a durable, append-only event log backed by a NATS JetStream stream
// — a networked backbone for multi-node HA. Unlike the SQLite/Postgres logs it
// needs no poller: the JetStream stream assigns each message a monotonic
// sequence (the event Seq, so appends from every node share one total order),
// and a push consumer delivers new messages — this node's and others' — to the
// in-process bus live.
type NATSLog struct {
	nc  *nats.Conn
	js  nats.JetStreamContext
	bus *bus
	sub *nats.Subscription

	mu     sync.Mutex
	closed bool
}

// OpenNATSLog connects to a NATS server (JetStream enabled), ensures the event
// stream exists, and starts the live push subscription.
func OpenNATSLog(url string) (*NATSLog, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("eventlog: nats connect: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("eventlog: nats jetstream: %w", err)
	}
	if _, err := js.StreamInfo(natsStream); errors.Is(err, nats.ErrStreamNotFound) {
		if _, err := js.AddStream(&nats.StreamConfig{
			Name:     natsStream,
			Subjects: []string{natsSubject},
			Storage:  nats.FileStorage,
			// The event log is the system of record: never age/size/count out an
			// event, or a projection rebuild would silently lose history.
			Retention: nats.LimitsPolicy,
			MaxAge:    0,
			MaxMsgs:   -1,
			MaxBytes:  -1,
			Discard:   nats.DiscardNew, // refuse new writes at a limit rather than drop old ones
		}); err != nil {
			nc.Close()
			return nil, fmt.Errorf("eventlog: nats add stream: %w", err)
		}
	} else if err != nil {
		nc.Close()
		return nil, fmt.Errorf("eventlog: nats stream info: %w", err)
	}

	l := &NATSLog{nc: nc, js: js, bus: newBus()}
	// Deliver only messages appended from now on (history is replayed via Read);
	// the push consumer is the live bus feed for every node's events.
	sub, err := js.Subscribe(natsSubject, l.onMessage, nats.DeliverNew(), nats.AckNone())
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("eventlog: nats subscribe: %w", err)
	}
	l.sub = sub
	return l, nil
}

func (l *NATSLog) onMessage(m *nats.Msg) {
	e, err := decodeEnvelope(m.Data)
	if err != nil {
		slog.Error("eventlog: nats decode", "err", err)
		return
	}
	meta, err := m.Metadata()
	if err != nil {
		slog.Error("eventlog: nats metadata", "err", err)
		return
	}
	e.Seq = meta.Sequence.Stream
	l.bus.publish(e)
}

// Append publishes the event; JetStream assigns its global Seq (the ack
// sequence). The push subscription — not Append — delivers it to the bus, so
// local and remote events arrive by the same path and never twice.
func (l *NATSLog) Append(ctx context.Context, e Envelope) (Envelope, error) {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return Envelope{}, ErrClosed
	}
	if e.Org == "" || e.Workspace == "" {
		return Envelope{}, fmt.Errorf("eventlog: event %q missing org/workspace", e.Type)
	}
	if e.ID == "" {
		e.ID = newID()
	}
	b, err := json.Marshal(e) // Seq is 0 here and overridden on read with the stream sequence
	if err != nil {
		return Envelope{}, fmt.Errorf("eventlog: nats marshal: %w", err)
	}
	ack, err := l.js.Publish(natsSubject, b, nats.Context(ctx))
	if err != nil {
		return Envelope{}, fmt.Errorf("eventlog: nats publish: %w", err)
	}
	e.Seq = ack.Sequence
	return e, nil
}

// Read returns all events with Seq >= fromSeq (0 = all), in order, by walking
// the stream sequence range.
func (l *NATSLog) Read(_ context.Context, fromSeq uint64) ([]Envelope, error) {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	head := l.Head()
	if head == 0 {
		return nil, nil
	}
	if fromSeq == 0 {
		fromSeq = 1
	}
	var out []Envelope
	for seq := fromSeq; seq <= head; seq++ {
		msg, err := l.js.GetMsg(natsStream, seq)
		if errors.Is(err, nats.ErrMsgNotFound) {
			// The log is the system of record and is configured for unlimited
			// retention, so a missing sequence in [fromSeq, head] is corruption,
			// not an expected gap — fail loudly rather than silently drop the event
			// (which would let a projection rebuild diverge undetectably).
			return nil, fmt.Errorf("eventlog: nats sequence %d missing within [%d,%d]: event log integrity violation", seq, fromSeq, head)
		}
		if err != nil {
			return nil, fmt.Errorf("eventlog: nats get msg %d: %w", seq, err)
		}
		e, err := decodeEnvelope(msg.Data)
		if err != nil {
			return nil, err
		}
		e.Seq = msg.Sequence
		out = append(out, e)
	}
	return out, nil
}

// Subscribe returns events the push consumer delivers after the call.
func (l *NATSLog) Subscribe() (<-chan Envelope, func()) { return l.bus.subscribe() }

// Head returns the highest assigned Seq (0 when empty).
func (l *NATSLog) Head() uint64 {
	si, err := l.js.StreamInfo(natsStream)
	if err != nil {
		slog.Error("eventlog: nats head", "err", err)
		return 0
	}
	return si.State.LastSeq
}

// Close unsubscribes, closes the bus, and closes the connection.
func (l *NATSLog) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()

	if l.sub != nil {
		_ = l.sub.Unsubscribe()
	}
	l.bus.closeAll()
	l.nc.Close()
	return nil
}

func decodeEnvelope(data []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(data, &e); err != nil {
		return Envelope{}, fmt.Errorf("eventlog: nats decode envelope: %w", err)
	}
	return e, nil
}
