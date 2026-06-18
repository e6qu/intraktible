// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

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
}

// OpenPostgresLog connects to dsn, ensures the events table exists, and starts
// the delivery poller.
func OpenPostgresLog(ctx context.Context, dsn string, poll time.Duration) (*PostgresLog, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open postgres: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS events (
		seq       BIGSERIAL PRIMARY KEY,
		id        TEXT NOT NULL,
		org       TEXT NOT NULL,
		workspace TEXT NOT NULL,
		stream    TEXT NOT NULL,
		type      TEXT NOT NULL,
		time      TEXT NOT NULL,
		actor     TEXT NOT NULL,
		payload   TEXT NOT NULL
	)`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("eventlog: postgres schema: %w", err)
	}
	l := &PostgresLog{pool: pool}
	head, err := l.headCtx(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}
	l.d = startDelivery(l.Read, poll, head)
	return l, nil
}

// Append assigns the next global Seq (BIGSERIAL) and commits durably. The poller
// — not Append — publishes to the bus, so local and cross-node events arrive by
// the same path and are never delivered twice.
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
		`INSERT INTO events (id, org, workspace, stream, type, time, actor, payload)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING seq`,
		e.ID, e.Org, e.Workspace, e.Stream, e.Type, e.Time.Format(time.RFC3339Nano), e.Actor, string(e.Payload),
	).Scan(&seq)
	if err != nil {
		return Envelope{}, fmt.Errorf("eventlog: postgres append: %w", err)
	}
	if seq <= 0 {
		return Envelope{}, fmt.Errorf("eventlog: postgres returned non-positive seq %d", seq)
	}
	e.Seq = uint64(seq)
	return e, nil
}

// Read returns all events with Seq >= fromSeq (0 = all), in order.
func (l *PostgresLog) Read(ctx context.Context, fromSeq uint64) ([]Envelope, error) {
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
		 FROM events WHERE seq >= $1 ORDER BY seq`, int64(fromSeq))
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

// Close stops the poller, closes subscriptions, and closes the pool.
func (l *PostgresLog) Close() error {
	return l.d.stopAndClose(func() error {
		l.pool.Close()
		return nil
	})
}
