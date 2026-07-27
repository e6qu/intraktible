// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !js

package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

const (
	natsStream  = "INTRAKTIBLE_EVENTS"
	natsSubject = "intraktible.events"
	// claimDedupWindow is the JetStream Msg-Id dedup window backing Envelope.Unique
	// optimistic-concurrency claims. It only needs to span the race between two
	// nodes computing the same claim (seconds); 5 minutes is generous headroom.
	claimDedupWindow = 5 * time.Minute
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

	mu sync.Mutex
	// deliverErr latches the first live-delivery failure. The push subscription is
	// the ONLY path onto the bus for this backend — there is no poller behind it —
	// so a message that cannot be delivered is not a hiccup to log past: it leaves
	// this replica's read models permanently behind with nothing else to notice.
	deliverErr error
	closed     bool
	lastSeq    uint64 // highest stream seq delivered to the bus (reconnect resume point)
}

// OpenNATSLog connects to a NATS server (JetStream enabled), ensures the event
// stream exists, and starts the live push subscription.
func OpenNATSLog(url string) (*NATSLog, error) {
	l := &NATSLog{bus: newBus()}
	// Reconnect with no cap (the log is the system of record) and, on reconnect,
	// re-subscribe from the last delivered seq so events appended while the
	// connection was down are still delivered — the ephemeral DeliverNew consumer
	// would otherwise restart at "new" and silently skip the gap.
	nc, err := nats.Connect(url,
		nats.MaxReconnects(-1),
		nats.ReconnectHandler(func(*nats.Conn) { l.onReconnect() }),
	)
	if err != nil {
		return nil, fmt.Errorf("eventlog: nats connect: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("eventlog: nats jetstream: %w", err)
	}
	si, err := js.StreamInfo(natsStream)
	switch {
	case errors.Is(err, nats.ErrStreamNotFound):
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
			// Duplicates enables Msg-Id dedup, which backs Envelope.Unique optimistic-
			// concurrency claims (a second append with the same claim id inside the
			// window is rejected as a duplicate → ErrConflict). The window only has to
			// cover the brief race between two nodes computing the same claim.
			Duplicates: claimDedupWindow,
		}); err != nil {
			nc.Close()
			return nil, fmt.Errorf("eventlog: nats add stream: %w", err)
		}
	case err != nil:
		nc.Close()
		return nil, fmt.Errorf("eventlog: nats stream info: %w", err)
	case si.Config.Duplicates < claimDedupWindow:
		// A pre-existing stream needs its dedup window widened, because that window IS
		// the enforcement of Envelope.Unique on this backend. Failing to widen it and
		// carrying on left optimistic-concurrency claims silently unenforced — two
		// nodes racing to create the same flow slug would both succeed, which is the
		// exact outcome the claim exists to prevent, reported as a log line nobody
		// reads. Refuse to open instead.
		cfg := si.Config
		cfg.Duplicates = claimDedupWindow
		if _, err := js.UpdateStream(&cfg); err != nil {
			nc.Close()
			return nil, fmt.Errorf("eventlog: nats dedup window is %s, shorter than the %s that optimistic-concurrency claims require, and it could not be widened: %w",
				si.Config.Duplicates, claimDedupWindow, err)
		}
	}

	l.nc = nc
	l.js = js
	// Deliver only messages appended from now on (history is replayed via Read);
	// the push consumer is the live bus feed for every node's events. resubscribe(0)
	// starts at "new"; a reconnect later resumes from the last delivered seq.
	if err := l.resubscribe(0); err != nil {
		nc.Close()
		return nil, fmt.Errorf("eventlog: nats subscribe: %w", err)
	}
	return l, nil
}

// resubscribe (re)creates the push subscription. afterSeq 0 means "from new"; a
// positive afterSeq resumes from afterSeq+1, the gap-fill used on reconnect.
func (l *NATSLog) resubscribe(afterSeq uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	if l.sub != nil {
		_ = l.sub.Unsubscribe()
		l.sub = nil
	}
	deliver := nats.DeliverNew()
	if afterSeq > 0 {
		deliver = nats.StartSequence(afterSeq + 1)
	}
	sub, err := l.js.Subscribe(natsSubject, l.onMessage, deliver, nats.AckNone())
	if err != nil {
		return err
	}
	l.sub = sub
	return nil
}

// onReconnect re-subscribes from the last delivered seq so events appended during
// the disconnect window are delivered (the SQLite/Postgres pollers self-heal the
// same way; the ephemeral DeliverNew consumer would skip the gap).
func (l *NATSLog) onReconnect() {
	l.mu.Lock()
	last, closed := l.lastSeq, l.closed
	l.mu.Unlock()
	if closed {
		return
	}
	if err := l.resubscribe(last); err != nil {
		slog.Error("eventlog: nats resubscribe after reconnect", "err", err)
	}
}

func (l *NATSLog) onMessage(m *nats.Msg) {
	e, err := decodeEnvelope(m.Data)
	if err != nil {
		l.failDelivery(fmt.Errorf("decode delivered event: %w", err))
		return
	}
	meta, err := m.Metadata()
	if err != nil {
		l.failDelivery(fmt.Errorf("read delivered event metadata: %w", err))
		return
	}
	e.Seq = meta.Sequence.Stream
	l.mu.Lock()
	if e.Seq > l.lastSeq {
		l.lastSeq = e.Seq
	}
	l.mu.Unlock()
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
	opts := []nats.PubOpt{nats.Context(ctx)}
	if e.Unique != "" {
		opts = append(opts, nats.MsgId(e.Unique)) // JetStream dedups by Msg-Id within the window
	}
	ack, err := l.js.Publish(natsSubject, b, opts...)
	if err != nil {
		return Envelope{}, fmt.Errorf("eventlog: nats publish: %w", err)
	}
	if e.Unique != "" && ack.Duplicate {
		// A duplicate Msg-Id means another node already claimed this key.
		return Envelope{}, ErrConflict
	}
	e.Seq = ack.Sequence
	return e, nil
}

// Read returns all events with Seq >= fromSeq (0 = all), in order, by walking
// the stream sequence range.
// ReadTenantStream reads then filters (the JetStream consumer has no tenant/stream
// index of its own; the durable SQL projection stores are where scale reads live).
func (l *NATSLog) ReadTenantStream(ctx context.Context, org, workspace, stream string, fromSeq uint64) ([]Envelope, error) {
	evs, err := l.Read(ctx, fromSeq)
	if err != nil {
		return nil, err
	}
	return filterTenantStream(evs, org, workspace, stream), nil
}

func (l *NATSLog) Read(_ context.Context, fromSeq uint64) ([]Envelope, error) {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	// One StreamInfo gives both bounds consistently — no separate Head() RPC to
	// race against (a TOCTOU where the head advances between the two calls). Clamp
	// fromSeq up to FirstSeq rather than assuming 1, so a purged prefix (if
	// retention is ever bounded) reads cleanly instead of tripping the gap check.
	si, err := l.js.StreamInfo(natsStream)
	if err != nil {
		return nil, fmt.Errorf("eventlog: nats stream info: %w", err)
	}
	first, last := si.State.FirstSeq, si.State.LastSeq
	if last == 0 {
		return nil, nil
	}
	if fromSeq < first {
		fromSeq = first
	}
	var out []Envelope
	for seq := fromSeq; seq <= last; seq++ {
		msg, err := l.js.GetMsg(natsStream, seq)
		if errors.Is(err, nats.ErrMsgNotFound) {
			// Within [first,last] every sequence must exist: only a gap strictly
			// inside the live range is corruption, not an expected gap — fail loudly
			// rather than silently drop the event (which would let a projection
			// rebuild diverge undetectably).
			return nil, fmt.Errorf("eventlog: nats sequence %d missing within [%d,%d]: event log integrity violation", seq, first, last)
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

// failDelivery latches a live-delivery failure and logs it.
//
// Skipping the message and carrying on was the previous behavior, and it hid the
// worst case rather than the rarest: a message that will not decode almost always
// means a producer/version mismatch, so every following message fails the same way,
// nothing ever reaches the bus, and the replica serves indefinitely stale read
// models while /healthz and /readyz both report fine. Latching it lets the health
// probe say what is actually true.
func (l *NATSLog) failDelivery(err error) {
	l.mu.Lock()
	if l.deliverErr == nil {
		l.deliverErr = err
	}
	l.mu.Unlock()
	slog.Error("eventlog: nats live delivery failed; this replica's read models will fall behind", "err", err)
}

// Err reports the first live-delivery failure, if any. The composition root folds it
// into /healthz so an orchestrator can replace a replica whose bus has gone silent.
func (l *NATSLog) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.deliverErr
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
