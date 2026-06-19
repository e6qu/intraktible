// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (CGO-free); registers "sqlite"
)

// DefaultPollInterval is how often a SQLiteLog polls for events appended by other
// processes (and itself) to deliver to in-process subscribers.
const DefaultPollInterval = 200 * time.Millisecond

// SQLiteLog is a durable, append-only event log backed by a single SQLite file
// that MULTIPLE PROCESSES can share — the missing piece for the split-services
// profile (D18), where each module runs in its own process. Seq is a global
// AUTOINCREMENT primary key, so appends from any process stay totally ordered;
// SQLite's WAL mode + busy timeout serialize concurrent writers.
//
// Cross-process delivery: SQLite has no pub/sub, so a background poller reads
// newly-committed rows (its own and other processes') and publishes them to the
// in-process bus that Subscribe hands out. Live delivery is therefore eventual,
// bounded by the poll interval; Read/Head are always immediately consistent. The
// projection runtime rebuilds from the log on boot and consumes the bus for live
// updates, so a poll-interval delay is just projection lag, not data loss.
type SQLiteLog struct {
	db *sql.DB
	d  *delivery
}

// OpenSQLiteLog opens (creating if needed) a shared SQLite event log at
// dir/events-log.db and starts its delivery poller.
func OpenSQLiteLog(dir string, poll time.Duration) (*SQLiteLog, error) {
	if poll <= 0 {
		poll = DefaultPollInterval
	}
	// dir is operator config (--data-dir), the filename is constant.
	path := filepath.Join(dir, "events-log.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open sqlite %q: %w", path, err)
	}
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("eventlog: sqlite %s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS events (
		seq       INTEGER PRIMARY KEY AUTOINCREMENT,
		id        TEXT NOT NULL,
		org       TEXT NOT NULL,
		workspace TEXT NOT NULL,
		stream    TEXT NOT NULL,
		type      TEXT NOT NULL,
		time      TEXT NOT NULL,
		actor     TEXT NOT NULL,
		payload   TEXT NOT NULL
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("eventlog: sqlite schema: %w", err)
	}
	l := &SQLiteLog{db: db}
	head, err := l.headCtx(context.Background())
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	// Seed at the current head so the poller delivers only events appended from now
	// on (the runtime rebuilds history via Read; the bus is for live updates).
	l.d = newDelivery(l.readFrom, poll, head)
	l.d.start()
	return l, nil
}

// Append assigns the next global Seq (AUTOINCREMENT) and commits durably. The
// poller — not Append — publishes to the bus, so local and remote events arrive
// by the same path and are never delivered twice.
func (l *SQLiteLog) Append(ctx context.Context, e Envelope) (Envelope, error) {
	if l.d.isClosed() {
		return Envelope{}, ErrClosed
	}
	if e.Org == "" || e.Workspace == "" {
		return Envelope{}, fmt.Errorf("eventlog: event %q missing org/workspace", e.Type)
	}
	if e.ID == "" {
		e.ID = newID()
	}
	res, err := l.db.ExecContext(ctx,
		`INSERT INTO events (id, org, workspace, stream, type, time, actor, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Org, e.Workspace, e.Stream, e.Type, e.Time.Format(time.RFC3339Nano), e.Actor, string(e.Payload))
	if err != nil {
		return Envelope{}, fmt.Errorf("eventlog: sqlite append: %w", err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return Envelope{}, fmt.Errorf("eventlog: sqlite append seq: %w", err)
	}
	if seq <= 0 {
		return Envelope{}, fmt.Errorf("eventlog: sqlite returned non-positive seq %d", seq)
	}
	e.Seq = uint64(seq)
	return e, nil
}

// Read returns all events with Seq >= fromSeq (0 = all), in order.
func (l *SQLiteLog) Read(ctx context.Context, fromSeq uint64) ([]Envelope, error) {
	return l.readFrom(ctx, fromSeq, 0)
}

// readFrom returns events with Seq >= fromSeq in order, capped at limit rows
// (limit <= 0 = unbounded). The poller passes a bound so one pass never loads
// the whole unread tail into memory; Read passes 0 for full replay.
func (l *SQLiteLog) readFrom(ctx context.Context, fromSeq uint64, limit int) ([]Envelope, error) {
	if l.d.isClosed() {
		return nil, ErrClosed
	}
	if fromSeq == 0 {
		fromSeq = 1
	}
	query := `SELECT seq, id, org, workspace, stream, type, time, actor, payload
		 FROM events WHERE seq >= ? ORDER BY seq`
	args := []any{fromSeq}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("eventlog: sqlite read: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Envelope
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("eventlog: sqlite read rows: %w", err)
	}
	return out, nil
}

// scanEvent reads one row into an Envelope.
func scanEvent(rows *sql.Rows) (Envelope, error) {
	var e Envelope
	var ts, payload string
	if err := rows.Scan(&e.Seq, &e.ID, &e.Org, &e.Workspace, &e.Stream, &e.Type, &ts, &e.Actor, &payload); err != nil {
		return Envelope{}, fmt.Errorf("eventlog: sqlite scan: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return Envelope{}, fmt.Errorf("eventlog: sqlite parse time at seq %d: %w", e.Seq, err)
	}
	e.Time = t
	e.Payload = []byte(payload)
	return e, nil
}

// Subscribe returns events the poller publishes after the call.
func (l *SQLiteLog) Subscribe() (<-chan Envelope, func()) { return l.d.subscribe() }

// Head returns the highest assigned Seq (0 when empty).
func (l *SQLiteLog) Head() uint64 {
	h, err := l.headCtx(context.Background())
	if err != nil {
		slog.Error("eventlog: sqlite head failed", "err", err)
		return 0
	}
	return h
}

func (l *SQLiteLog) headCtx(ctx context.Context) (uint64, error) {
	var head sql.NullInt64
	if err := l.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM events`).Scan(&head); err != nil {
		return 0, fmt.Errorf("eventlog: sqlite head: %w", err)
	}
	if !head.Valid || head.Int64 < 0 {
		return 0, nil
	}
	return uint64(head.Int64), nil
}

// Close stops the poller, closes subscriptions, and closes the database.
func (l *SQLiteLog) Close() error { return l.d.stopAndClose(l.db.Close) }
