// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (CGO-free); registers "sqlite"
)

// SQLite is a durable Store backed by a single SQLite file: projections survive a
// restart, and the projection runtime can still rebuild them from the log. It
// holds every collection's documents in one table keyed by (collection, key).
// One projection goroutine writes while HTTP handlers read, so it runs in WAL mode
// with a busy timeout (concurrent readers + a single writer).
type SQLite struct {
	db *sql.DB
}

// NewSQLite opens (creating if needed) the SQLite store at path.
func NewSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite %q: %w", path, err)
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("store: sqlite %s: %w", pragma, err)
		}
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

func (s *SQLite) List(ctx context.Context, collection string) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, doc FROM docs WHERE collection = ? ORDER BY key`, collection)
	if err != nil {
		return nil, fmt.Errorf("store: sqlite list %s: %w", collection, err)
	}
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
	_, err := s.db.ExecContext(ctx, `DELETE FROM docs WHERE collection = ? AND key = ?`, collection, key)
	if err != nil {
		return fmt.Errorf("store: sqlite delete %s/%s: %w", collection, key, err)
	}
	return nil
}

func (s *SQLite) Reset(ctx context.Context, collection string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM docs WHERE collection = ?`, collection)
	if err != nil {
		return fmt.Errorf("store: sqlite reset %s: %w", collection, err)
	}
	return nil
}

// Close closes the underlying database.
func (s *SQLite) Close() error { return s.db.Close() }
