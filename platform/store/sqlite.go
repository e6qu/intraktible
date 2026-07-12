// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !js

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (CGO-free); registers "sqlite"
)

// SQLite is a durable Store backed by a single SQLite file: projections survive a
// restart, and the projection runtime can still rebuild them from the log. It
// holds every collection's documents in one table keyed by (collection, key).
// One projection goroutine writes while HTTP handlers read, so it runs in WAL mode
// with a busy timeout (concurrent readers + a single writer).
type SQLite struct {
	db *sql.DB
	// wmu serializes writers. SQLite admits only one writer at a time; without
	// this a second concurrent write transaction trips SQLITE_LOCKED (which the
	// busy timeout does not retry) instead of waiting its turn. Readers (Get/List)
	// don't take it, so WAL still serves them concurrently with the writer.
	wmu sync.Mutex
}

// NewSQLite opens (creating if needed) the SQLite store at path.
func NewSQLite(path string) (*SQLite, error) {
	// Apply the pragmas via the DSN so they take on EVERY pooled connection — a
	// one-off `db.Exec("PRAGMA …")` only configures whichever connection served
	// it, leaving the rest of the pool without the busy timeout (so a second
	// writer would get SQLITE_BUSY immediately instead of waiting its turn).
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite %q: %w", path, err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS docs (
		collection TEXT NOT NULL,
		key        TEXT NOT NULL,
		doc        TEXT NOT NULL,
		PRIMARY KEY (collection, key)
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: sqlite schema: %w", err)
	}
	return &SQLite{db: db}, nil
}

func (s *SQLite) Put(ctx context.Context, collection, key string, doc json.RawMessage) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO docs (collection, key, doc) VALUES (?, ?, ?)
		 ON CONFLICT (collection, key) DO UPDATE SET doc = excluded.doc`,
		collection, key, string(doc))
	if err != nil {
		return fmt.Errorf("store: sqlite put %s/%s: %w", collection, key, err)
	}
	return nil
}

func (s *SQLite) Get(ctx context.Context, collection, key string) (json.RawMessage, bool, error) {
	var doc string
	err := s.db.QueryRowContext(ctx,
		`SELECT doc FROM docs WHERE collection = ? AND key = ?`, collection, key).Scan(&doc)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: sqlite get %s/%s: %w", collection, key, err)
	}
	return json.RawMessage(doc), true, nil
}

func (s *SQLite) List(ctx context.Context, collection, keyPrefix string) ([]Record, error) {
	q, args := listQuerySQLite(collection, keyPrefix)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: sqlite list %s: %w", collection, err)
	}
	return scanSQLRecords(rows, collection)
}

// listQuerySQLite builds the prefix-scoped list query for the ?-placeholder SQLite
// backend: an indexed `key >= prefix AND key < upper` range, or the whole
// collection when keyPrefix is empty.
func listQuerySQLite(collection, keyPrefix string) (string, []any) {
	q := `SELECT key, doc FROM docs WHERE collection = ?`
	args := []any{collection}
	if keyPrefix != "" {
		q += ` AND key >= ?`
		args = append(args, keyPrefix)
		if ub := prefixUpperBound(keyPrefix); ub != "" {
			q += ` AND key < ?`
			args = append(args, ub)
		}
	}
	return q + ` ORDER BY key`, args
}

// scanSQLRecords reads (key, doc) rows into Records and closes the rows. Shared by
// the store and its transaction so the scan loop lives in one place.
func scanSQLRecords(rows *sql.Rows, collection string) ([]Record, error) {
	defer func() { _ = rows.Close() }()
	var out []Record
	for rows.Next() {
		var key, doc string
		if err := rows.Scan(&key, &doc); err != nil {
			return nil, fmt.Errorf("store: sqlite scan %s: %w", collection, err)
		}
		out = append(out, Record{Key: key, Doc: json.RawMessage(doc)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: sqlite rows %s: %w", collection, err)
	}
	return out, nil
}

func (s *SQLite) Delete(ctx context.Context, collection, key string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM docs WHERE collection = ? AND key = ?`, collection, key)
	if err != nil {
		return fmt.Errorf("store: sqlite delete %s/%s: %w", collection, key, err)
	}
	return nil
}

func (s *SQLite) Reset(ctx context.Context, collection string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM docs WHERE collection = ?`, collection)
	if err != nil {
		return fmt.Errorf("store: sqlite reset %s: %w", collection, err)
	}
	return nil
}

// Begin starts a transaction; its writes commit or roll back atomically. It holds
// the writer lock for the transaction's lifetime (released on Commit/Rollback) so
// only one writer is ever in flight.
func (s *SQLite) Begin(ctx context.Context) (Tx, error) {
	s.wmu.Lock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.wmu.Unlock()
		return nil, fmt.Errorf("store: sqlite begin: %w", err)
	}
	return &sqliteTx{tx: tx, ctx: ctx, unlock: s.wmu.Unlock}, nil
}

// Close closes the underlying database.
func (s *SQLite) Close() error { return s.db.Close() }

// sqliteTx is a Store backed by an open *sql.Tx (read-your-writes within the tx).
type sqliteTx struct {
	tx     *sql.Tx
	ctx    context.Context
	unlock func() // releases the store's writer lock; idempotent via once
	once   sync.Once
}

func (t *sqliteTx) release() {
	if t.unlock != nil {
		t.once.Do(t.unlock)
	}
}

func (t *sqliteTx) Put(ctx context.Context, collection, key string, doc json.RawMessage) error {
	_, err := t.tx.ExecContext(ctx,
		`INSERT INTO docs (collection, key, doc) VALUES (?, ?, ?)
		 ON CONFLICT (collection, key) DO UPDATE SET doc = excluded.doc`,
		collection, key, string(doc))
	if err != nil {
		return fmt.Errorf("store: sqlite tx put %s/%s: %w", collection, key, err)
	}
	return nil
}

// PutIfAbsent inserts the doc only when (collection,key) is not already present.
// SQLite serializes all transactions behind one writer lock, so this is race-free by
// construction; it exists so the projection bootstrap can create-if-missing the
// checkpoint row uniformly across the durable backends.
func (t *sqliteTx) PutIfAbsent(ctx context.Context, collection, key string, doc json.RawMessage) error {
	_, err := t.tx.ExecContext(ctx,
		`INSERT INTO docs (collection, key, doc) VALUES (?, ?, ?)
		 ON CONFLICT (collection, key) DO NOTHING`,
		collection, key, string(doc))
	if err != nil {
		return fmt.Errorf("store: sqlite tx put-if-absent %s/%s: %w", collection, key, err)
	}
	return nil
}

// GetForUpdate reads inside the transaction. SQLite has no SELECT … FOR UPDATE, but
// Begin holds the store's global writer lock for the tx's lifetime, so no other
// transaction can write between this read and the tx's commit — the exclusivity a row
// lock would give, provided by the writer lock instead.
func (t *sqliteTx) GetForUpdate(ctx context.Context, collection, key string) (json.RawMessage, bool, error) {
	return t.Get(ctx, collection, key)
}

func (t *sqliteTx) Get(ctx context.Context, collection, key string) (json.RawMessage, bool, error) {
	var doc string
	err := t.tx.QueryRowContext(ctx,
		`SELECT doc FROM docs WHERE collection = ? AND key = ?`, collection, key).Scan(&doc)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: sqlite tx get %s/%s: %w", collection, key, err)
	}
	return json.RawMessage(doc), true, nil
}

func (t *sqliteTx) List(ctx context.Context, collection, keyPrefix string) ([]Record, error) {
	q, args := listQuerySQLite(collection, keyPrefix)
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: sqlite tx list %s: %w", collection, err)
	}
	return scanSQLRecords(rows, collection)
}

func (t *sqliteTx) Delete(ctx context.Context, collection, key string) error {
	_, err := t.tx.ExecContext(ctx, `DELETE FROM docs WHERE collection = ? AND key = ?`, collection, key)
	if err != nil {
		return fmt.Errorf("store: sqlite tx delete %s/%s: %w", collection, key, err)
	}
	return nil
}

func (t *sqliteTx) Reset(ctx context.Context, collection string) error {
	_, err := t.tx.ExecContext(ctx, `DELETE FROM docs WHERE collection = ?`, collection)
	if err != nil {
		return fmt.Errorf("store: sqlite tx reset %s: %w", collection, err)
	}
	return nil
}

func (t *sqliteTx) Commit() error   { defer t.release(); return t.tx.Commit() }
func (t *sqliteTx) Rollback() error { defer t.release(); return t.tx.Rollback() }

// Close is a no-op on a tx (Commit/Rollback end it); it exists to satisfy Store.
func (t *sqliteTx) Close() error { return nil }
