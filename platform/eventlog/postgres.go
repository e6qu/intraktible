// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !js

package eventlog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgNotifyChannel is the LISTEN/NOTIFY channel used to push "new events" hints
// across nodes so live delivery does not wait out the poll interval.
const pgNotifyChannel = "intraktible_events"

// PostgresLog is a durable, append-only event log backed by PostgreSQL — the
// networked backbone for true multi-node HA, where several intraktible processes
// (not just one box's split-services profile) share one ordered log. Seq is a
// BIGSERIAL primary key, so the database serializes appends from every node into
// one total order; Read/Head are immediately consistent.
//
// Postgres has the same no-native-pubsub constraint as SQLite, so live delivery
// to in-process subscribers is via a polling delivery (see delivery.go): the
// poller reads newly-committed rows from any node and publishes them to the bus.
// A LISTEN/NOTIFY fast path is a future optimization; the poller is the floor.
type PostgresLog struct {
	pool *pgxpool.Pool
	d    *delivery

	// A dedicated connection LISTENs for cross-node "new events" notifications and
	// pokes delivery; cancel/lwg stop it on Close. The poller runs regardless, so
	// the listener is purely a latency optimization.
	listen *pgx.Conn
	cancel context.CancelFunc
	lwg    sync.WaitGroup
}

// OpenPostgresLog connects to dsn, ensures the events table exists, and starts
// the delivery poller.
func OpenPostgresLog(ctx context.Context, dsn string, poll time.Duration) (*PostgresLog, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open postgres: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS events (
		seq        BIGSERIAL PRIMARY KEY,
		id         TEXT NOT NULL,
		org        TEXT NOT NULL,
		workspace  TEXT NOT NULL,
		stream     TEXT NOT NULL,
		type       TEXT NOT NULL,
		time       TEXT NOT NULL,
		actor      TEXT NOT NULL,
		payload    TEXT NOT NULL,
		unique_key TEXT
	)`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("eventlog: postgres schema: %w", err)
	}
	// Backfill unique_key on a pre-existing table, then the partial unique index that
	// enforces optimistic-concurrency claims (Envelope.Unique) across nodes.
	if _, err := pool.Exec(ctx, `ALTER TABLE events ADD COLUMN IF NOT EXISTS unique_key TEXT`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("eventlog: postgres add unique_key: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS events_tenant_stream ON events(org, workspace, stream, seq)`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("eventlog: postgres tenant-stream index: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS events_unique_key ON events(unique_key) WHERE unique_key IS NOT NULL`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("eventlog: postgres unique_key index: %w", err)
	}
	l := &PostgresLog{pool: pool}
	head, err := l.headCtx(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}
	l.d = newDelivery(l.readFrom, poll, head)
	l.d.start()
	// Start the LISTEN fast path. If it can't start, delivery still works via the
	// poller — so a listener failure degrades latency, not correctness.
	if err := l.startListener(ctx, dsn); err != nil {
		slog.Warn("eventlog: postgres LISTEN unavailable, using poll-only delivery", "err", err)
	}
	return l, nil
}

// startListener opens a dedicated connection, LISTENs on the notify channel, and
// pokes delivery on each notification so newly-committed events (from any node)
// are delivered without waiting for the next poll.
func (l *PostgresLog) startListener(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "LISTEN "+pgNotifyChannel); err != nil {
		_ = conn.Close(ctx)
		return err
	}
	lctx, cancel := context.WithCancel(context.Background())
	l.listen = conn
	l.cancel = cancel
	l.lwg.Add(1)
	go l.listenLoop(lctx)
	return nil
}

func (l *PostgresLog) listenLoop(ctx context.Context) {
	defer l.lwg.Done()
	for {
		if _, err := l.listen.WaitForNotification(ctx); err != nil {
			return // Close canceled us, or the connection dropped — the poller carries on.
		}
		l.d.poke()
	}
}

// Append assigns the next global Seq (BIGSERIAL) and commits durably. The poller
// — not Append — publishes to the bus, so local and cross-node events arrive by
// the same path and are never delivered twice.
//
// KNOWN LIMITATION (concurrent appends — multi-node HA, or a single node under
// concurrent request load): BIGSERIAL is assigned at INSERT but a row is visible only
// at COMMIT, so a higher seq can commit before a lower one. The poller advances its
// watermark by the max seq it has read, so a lower seq that commits late can be
// missed on the LIVE bus. The projection runtime now REFUSES to advance its checkpoint
// past such a gap (projection.applyContiguous fails loud rather than skipping the
// missing seq — previously it advanced past it, which permanently dropped the event
// from the incremental read model, not merely a latency gap): the projection surfaces
// the error via /healthz and re-applies the range once the lower seq is visible (on
// the next poll or a restart, which resumes from the intact checkpoint). The complete
// fix gates the POLLER watermark on the transaction-visibility horizon
// (pg_snapshot_xmin) so the gap never reaches the runtime; it is deliberately not
// shipped untested here (it needs a live multi-node Postgres to validate, and a naive
// contiguous watermark would deadlock on seqs burned by rolled-back transactions).
// Single-node file/sqlite/memory logs serialize appends and are unaffected.
func (l *PostgresLog) Append(ctx context.Context, e Envelope) (Envelope, error) {
	if l.d.isClosed() {
		return Envelope{}, ErrClosed
	}
	if e.Org == "" || e.Workspace == "" {
		return Envelope{}, fmt.Errorf("eventlog: event %q missing org/workspace", e.Type)
	}
	if e.ID == "" {
		e.ID = newID()
	}
	var seq int64
	err := l.pool.QueryRow(ctx,
		`INSERT INTO events (id, org, workspace, stream, type, time, actor, payload, unique_key)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING seq`,
		e.ID, e.Org, e.Workspace, e.Stream, e.Type, e.Time.Format(time.RFC3339Nano), e.Actor, string(e.Payload), nullableKey(e.Unique),
	).Scan(&seq)
	if err != nil {
		// 23505 = unique_violation: the caller lost an optimistic-concurrency race.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Envelope{}, ErrConflict
		}
		return Envelope{}, fmt.Errorf("eventlog: postgres append: %w", err)
	}
	if seq <= 0 {
		return Envelope{}, fmt.Errorf("eventlog: postgres returned non-positive seq %d", seq)
	}
	e.Seq = uint64(seq)
	// Best-effort push so other nodes' subscribers don't wait out the poll
	// interval. The poller is the correctness guarantee, so a NOTIFY error is not
	// fatal — delivery still happens, just at poll latency.
	_, _ = l.pool.Exec(ctx, "NOTIFY "+pgNotifyChannel)
	return e, nil
}

// Read returns all events with Seq >= fromSeq (0 = all), in order.
func (l *PostgresLog) Read(ctx context.Context, fromSeq uint64) ([]Envelope, error) {
	return l.readFrom(ctx, fromSeq, 0)
}

// ReadTenantStream returns one tenant's events on one stream with Seq >= fromSeq, in
// order — the indexed read the maker-checker folds use instead of scanning the whole
// log.
func (l *PostgresLog) ReadTenantStream(ctx context.Context, org, workspace, stream string, fromSeq uint64) ([]Envelope, error) {
	if l.d.isClosed() {
		return nil, ErrClosed
	}
	if fromSeq == 0 {
		fromSeq = 1
	}
	if fromSeq > math.MaxInt64 {
		return nil, fmt.Errorf("eventlog: postgres fromSeq %d out of range", fromSeq)
	}
	rows, err := l.pool.Query(ctx,
		`SELECT seq, id, org, workspace, stream, type, time, actor, payload
		 FROM events WHERE org = $1 AND workspace = $2 AND stream = $3 AND seq >= $4 ORDER BY seq`,
		org, workspace, stream, int64(fromSeq))
	if err != nil {
		return nil, fmt.Errorf("eventlog: postgres read tenant-stream: %w", err)
	}
	defer rows.Close()
	var out []Envelope
	for rows.Next() {
		var e Envelope
		var seq int64
		var ts, payload string
		if err := rows.Scan(&seq, &e.ID, &e.Org, &e.Workspace, &e.Stream, &e.Type, &ts, &e.Actor, &payload); err != nil {
			return nil, fmt.Errorf("eventlog: postgres scan tenant-stream: %w", err)
		}
		if seq <= 0 {
			return nil, fmt.Errorf("eventlog: postgres non-positive seq %d", seq)
		}
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, fmt.Errorf("eventlog: postgres parse time at seq %d: %w", seq, err)
		}
		e.Seq = uint64(seq)
		e.Time = t
		e.Payload = []byte(payload)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("eventlog: postgres read tenant-stream rows: %w", err)
	}
	return out, nil
}

// readFrom returns events with Seq >= fromSeq in order, capped at limit rows
// (limit <= 0 = unbounded). The poller passes a bound so one pass never loads
// the whole unread tail into memory; Read passes 0 for full replay.
func (l *PostgresLog) readFrom(ctx context.Context, fromSeq uint64, limit int) ([]Envelope, error) {
	if l.d.isClosed() {
		return nil, ErrClosed
	}
	if fromSeq == 0 {
		fromSeq = 1
	}
	if fromSeq > math.MaxInt64 {
		return nil, fmt.Errorf("eventlog: postgres fromSeq %d out of range", fromSeq)
	}
	query := `SELECT seq, id, org, workspace, stream, type, time, actor, payload
		 FROM events WHERE seq >= $1 ORDER BY seq`
	args := []any{int64(fromSeq)}
	if limit > 0 {
		query += ` LIMIT $2`
		args = append(args, limit)
	}
	rows, err := l.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("eventlog: postgres read: %w", err)
	}
	defer rows.Close()
	var out []Envelope
	for rows.Next() {
		var e Envelope
		var seq int64
		var ts, payload string
		if err := rows.Scan(&seq, &e.ID, &e.Org, &e.Workspace, &e.Stream, &e.Type, &ts, &e.Actor, &payload); err != nil {
			return nil, fmt.Errorf("eventlog: postgres scan: %w", err)
		}
		if seq <= 0 {
			return nil, fmt.Errorf("eventlog: postgres non-positive seq %d", seq)
		}
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, fmt.Errorf("eventlog: postgres parse time at seq %d: %w", seq, err)
		}
		e.Seq = uint64(seq)
		e.Time = t
		e.Payload = []byte(payload)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("eventlog: postgres read rows: %w", err)
	}
	return out, nil
}

// Subscribe returns events the poller publishes after the call.
func (l *PostgresLog) Subscribe() (<-chan Envelope, func()) { return l.d.subscribe() }

// Head returns the highest assigned Seq (0 when empty).
func (l *PostgresLog) Head() uint64 {
	h, err := l.headCtx(context.Background())
	if err != nil {
		slog.Error("eventlog: postgres head failed", "err", err)
		return 0
	}
	return h
}

func (l *PostgresLog) headCtx(ctx context.Context) (uint64, error) {
	var head *int64
	if err := l.pool.QueryRow(ctx, `SELECT MAX(seq) FROM events`).Scan(&head); err != nil {
		return 0, fmt.Errorf("eventlog: postgres head: %w", err)
	}
	if head == nil || *head < 0 {
		return 0, nil
	}
	return uint64(*head), nil
}

// Close stops the listener and poller, closes subscriptions, and closes the pool.
func (l *PostgresLog) Close() error {
	if l.cancel != nil {
		l.cancel()
		l.lwg.Wait()
		_ = l.listen.Close(context.Background())
	}
	return l.d.stopAndClose(func() error {
		l.pool.Close()
		return nil
	})
}
