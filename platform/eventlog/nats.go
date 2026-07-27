// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !js

package eventlog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

const (
	natsStream             = "INTRAKTIBLE_EVENTS"
	natsSubject            = "intraktible.events"
	natsClaimSubjectPrefix = natsSubject + ".claim."
	natsClaimSubjectFilter = natsClaimSubjectPrefix + "*"
	// claimDedupWindow is rolling-upgrade protection for Envelope.Unique: an older
	// node publishes a claim on natsSubject using Msg-Id, while current nodes use a
	// permanent per-subject CAS. The cache spans the brief mixed-version race; the
	// CAS, not this timer, enforces the whole-log lifetime promised by Log.
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
	// claims contains every Unique key observed in retained history or live
	// delivery, including legacy events on natsSubject. The permanent subject CAS
	// is authoritative across nodes; this set preserves pre-upgrade claims without
	// an O(total events) scan on every new append.
	claims map[string]struct{}
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
	return openNATSLog(url, claimDedupWindow)
}

func openNATSLog(url string, dedupWindow time.Duration) (*NATSLog, error) {
	l := &NATSLog{bus: newBus(), claims: make(map[string]struct{})}
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
			Name:         natsStream,
			Subjects:     []string{natsSubject, natsClaimSubjectFilter},
			Storage:      nats.FileStorage,
			MaxConsumers: -1,
			// The event log is the system of record: never age/size/count out an
			// event, or a projection rebuild would silently lose history.
			Retention:         nats.LimitsPolicy,
			MaxAge:            0,
			MaxMsgs:           -1,
			MaxBytes:          -1,
			MaxMsgsPerSubject: -1,
			Discard:           nats.DiscardNew, // refuse new writes at a limit rather than drop old ones
			DenyDelete:        true,
			DenyPurge:         true,
			// Duplicates covers a race against an older binary during a rolling
			// upgrade. Current binaries enforce Unique permanently with a per-claim
			// subject and ExpectLastSequencePerSubject(0).
			Duplicates: dedupWindow,
		}); err != nil {
			nc.Close()
			return nil, fmt.Errorf("eventlog: nats add stream: %w", err)
		}
	case err != nil:
		nc.Close()
		return nil, fmt.Errorf("eventlog: nats stream info: %w", err)
	default:
		if err := reconcileNATSStream(js, si, dedupWindow); err != nil {
			nc.Close()
			return nil, err
		}
	}

	l.nc, l.js = nc, js
	// Build the compatibility index before accepting writes, then subscribe from
	// exactly the following sequence. DeliverNew would leave a race between the
	// history scan and consumer creation where a legacy claim could be missed.
	scannedThrough, err := l.loadNATSClaims()
	if err != nil {
		nc.Close()
		return nil, err
	}
	if err := l.resubscribe(scannedThrough + 1); err != nil {
		nc.Close()
		return nil, fmt.Errorf("eventlog: nats subscribe: %w", err)
	}
	return l, nil
}

// reconcileNATSStream applies the same source-of-truth invariants to a
// pre-existing stream that Open creates for a new one. It can prevent future
// retention loss, but it cannot manufacture history already deleted: a prefix
// gap is therefore a hard startup error.
func reconcileNATSStream(js nats.JetStreamContext, si *nats.StreamInfo, dedupWindow time.Duration) error {
	cfg := si.Config
	if cfg.Storage != nats.FileStorage {
		return fmt.Errorf("eventlog: nats stream storage is %s, want file storage for a durable event log", cfg.Storage)
	}
	if cfg.Sealed {
		return errors.New("eventlog: nats stream is sealed and cannot accept events")
	}
	if si.State.FirstSeq > 1 {
		return fmt.Errorf("eventlog: nats retained history begins at sequence %d, want 1: event log prefix has been discarded", si.State.FirstSeq)
	}

	changed := false
	require := func(condition bool, apply func()) {
		if !condition {
			apply()
			changed = true
		}
	}
	require(cfg.Duplicates >= dedupWindow, func() { cfg.Duplicates = dedupWindow })
	require(containsNATSSubject(cfg.Subjects, natsSubject), func() {
		cfg.Subjects = append(cfg.Subjects, natsSubject)
	})
	require(containsNATSSubject(cfg.Subjects, natsClaimSubjectFilter), func() {
		cfg.Subjects = append(cfg.Subjects, natsClaimSubjectFilter)
	})
	require(cfg.Retention == nats.LimitsPolicy, func() { cfg.Retention = nats.LimitsPolicy })
	require(cfg.MaxConsumers == -1, func() { cfg.MaxConsumers = -1 })
	require(cfg.MaxAge == 0, func() { cfg.MaxAge = 0 })
	require(cfg.MaxMsgs == -1, func() { cfg.MaxMsgs = -1 })
	require(cfg.MaxBytes == -1, func() { cfg.MaxBytes = -1 })
	require(cfg.MaxMsgsPerSubject == -1, func() { cfg.MaxMsgsPerSubject = -1 })
	require(cfg.Discard == nats.DiscardNew, func() { cfg.Discard = nats.DiscardNew })
	require(!cfg.DiscardNewPerSubject, func() { cfg.DiscardNewPerSubject = false })
	require(!cfg.NoAck, func() { cfg.NoAck = false })
	require(cfg.DenyDelete, func() { cfg.DenyDelete = true })
	require(cfg.DenyPurge, func() { cfg.DenyPurge = true })
	if !changed {
		return nil
	}
	if _, err := js.UpdateStream(&cfg); err != nil {
		return fmt.Errorf("eventlog: nats stream cannot enforce durable permanent-claim settings and could not be updated: %w", err)
	}
	return nil
}

// resubscribe (re)creates the push subscription. startSeq 0 means "from new";
// otherwise delivery begins at that exact sequence.
func (l *NATSLog) resubscribe(startSeq uint64) error {
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
	if startSeq > 0 {
		deliver = nats.StartSequence(startSeq)
	}
	sub, err := l.js.Subscribe("", l.onMessage,
		deliver,
		nats.AckNone(),
		nats.BindStream(natsStream),
		nats.ConsumerFilterSubjects(natsSubject, natsClaimSubjectFilter),
	)
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
	if err := l.resubscribe(last + 1); err != nil {
		l.failDelivery(fmt.Errorf("resubscribe after reconnect: %w", err))
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
	if e.Unique != "" {
		l.claims[e.Unique] = struct{}{}
	}
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
	subject := natsSubject
	opts := []nats.PubOpt{nats.Context(ctx)}
	if e.Unique != "" {
		subject = natsClaimSubject(e.Unique)
		claimed, err := l.natsClaimed(e.Unique, subject)
		if err != nil {
			return Envelope{}, err
		}
		if claimed {
			return Envelope{}, ErrConflict
		}
		// Expecting no prior message on this deterministic subject is an atomic,
		// permanent compare-and-set: the claimed event itself is the index, so there
		// is no second store or reservation that can commit without its event.
		opts = append(opts,
			nats.MsgId(e.Unique),
			nats.ExpectLastSequencePerSubject(0),
		)
	}
	ack, err := l.js.Publish(subject, b, opts...)
	if err != nil {
		if e.Unique != "" && natsWrongLastSequence(err) {
			return Envelope{}, ErrConflict
		}
		return Envelope{}, fmt.Errorf("eventlog: nats publish: %w", err)
	}
	if e.Unique != "" && ack.Duplicate {
		// A duplicate Msg-Id means another node already claimed this key.
		return Envelope{}, ErrConflict
	}
	e.Seq = ack.Sequence
	if e.Unique != "" {
		l.mu.Lock()
		l.claims[e.Unique] = struct{}{}
		l.mu.Unlock()
	}
	return e, nil
}

func (l *NATSLog) natsClaimed(unique, subject string) (bool, error) {
	l.mu.Lock()
	_, claimed := l.claims[unique]
	l.mu.Unlock()
	if claimed {
		return true, nil
	}
	if _, err := l.js.GetLastMsg(natsStream, subject); err == nil {
		return true, nil
	} else if !errors.Is(err, nats.ErrMsgNotFound) {
		return false, fmt.Errorf("eventlog: nats read claim index: %w", err)
	}
	return false, nil
}

// loadNATSClaims makes every pre-upgrade base-subject claim part of the current
// in-memory compatibility index. The returned sequence is the exact boundary
// from which live delivery must start so no append can fall between scan and
// subscription.
func (l *NATSLog) loadNATSClaims() (uint64, error) {
	evs, err := l.Read(context.Background(), 0)
	if err != nil {
		return 0, fmt.Errorf("eventlog: nats load retained claims: %w", err)
	}
	var through uint64
	l.mu.Lock()
	for _, e := range evs {
		if e.Unique != "" {
			l.claims[e.Unique] = struct{}{}
		}
		through = e.Seq
	}
	l.lastSeq = through
	l.mu.Unlock()
	return through, nil
}

func natsClaimSubject(unique string) string {
	return natsClaimSubjectPrefix + base64.RawURLEncoding.EncodeToString([]byte(unique))
}

func natsWrongLastSequence(err error) bool {
	var apiErr *nats.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode == nats.JSErrCodeStreamWrongLastSequence
}

func containsNATSSubject(subjects []string, want string) bool {
	for _, subject := range subjects {
		if subject == want {
			return true
		}
	}
	return false
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
