// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres is a durable, shared Store backed by PostgreSQL (via pgx) — the option
// for large or multi-process projections where SQLite's single-file model is a
// bottleneck. Like the other stores it holds every collection's documents in one
// table keyed by (collection, key), with the document in a JSONB column. It is the
// drop-in the store.Store interface was designed for; selectable with
// `serve --store=postgres` + INTRAKTIBLE_POSTGRES_DSN.
//
// NOTE: this adapter is exercised by store_test.go only when INTRAKTIBLE_TEST_POSTGRES
// points at a real database (the deploy compose `pg` profile provides one). It is
// not run in the default CI here, which has no Postgres — see BUGS.md D21.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres connects to dsn and ensures the docs table exists.
func NewPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open postgres: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS docs (
		collection TEXT  NOT NULL,
		key        TEXT  NOT NULL,
		doc        JSONB NOT NULL,
		PRIMARY KEY (collection, key)
	)`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: postgres schema: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (s *Postgres) Put(ctx context.Context, collection, key string, doc json.RawMessage) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO docs (collection, key, doc) VALUES ($1, $2, $3)
		 ON CONFLICT (collection, key) DO UPDATE SET doc = EXCLUDED.doc`,
		collection, key, []byte(doc))
	if err != nil {
		return fmt.Errorf("store: postgres put %s/%s: %w", collection, key, err)
	}
	return nil
}

func (s *Postgres) Get(ctx context.Context, collection, key string) (json.RawMessage, bool, error) {
	var doc []byte
	err := s.pool.QueryRow(ctx,
		`SELECT doc FROM docs WHERE collection = $1 AND key = $2`, collection, key).Scan(&doc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: postgres get %s/%s: %w", collection, key, err)
	}
	return json.RawMessage(doc), true, nil
}

func (s *Postgres) List(ctx context.Context, collection string) ([]Record, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT key, doc FROM docs WHERE collection = $1 ORDER BY key`, collection)
	if err != nil {
		return nil, fmt.Errorf("store: postgres list %s: %w", collection, err)
	}
	return scanPGRecords(rows, collection)
}

// scanPGRecords reads (key, doc) rows into Records and closes the rows. Shared by
// the store and its transaction so the scan loop lives in one place.
func scanPGRecords(rows pgx.Rows, collection string) ([]Record, error) {
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var key string
		var doc []byte
		if err := rows.Scan(&key, &doc); err != nil {
			return nil, fmt.Errorf("store: postgres scan %s: %w", collection, err)
		}
		out = append(out, Record{Key: key, Doc: json.RawMessage(doc)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: postgres rows %s: %w", collection, err)
	}
	return out, nil
}

func (s *Postgres) Delete(ctx context.Context, collection, key string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM docs WHERE collection = $1 AND key = $2`, collection, key)
	if err != nil {
		return fmt.Errorf("store: postgres delete %s/%s: %w", collection, key, err)
	}
	return nil
}

func (s *Postgres) Reset(ctx context.Context, collection string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM docs WHERE collection = $1`, collection)
	if err != nil {
		return fmt.Errorf("store: postgres reset %s: %w", collection, err)
	}
	return nil
}

// Begin starts a transaction; its writes commit or roll back atomically.
func (s *Postgres) Begin(ctx context.Context) (Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: postgres begin: %w", err)
	}
	return &pgTx{tx: tx, ctx: ctx}, nil
}

// Close releases the connection pool.
func (s *Postgres) Close() error {
	s.pool.Close()
	return nil
}

// pgTx is a Store backed by an open pgx.Tx (read-your-writes within the tx).
type pgTx struct {
	tx  pgx.Tx
	ctx context.Context
}

func (t *pgTx) Put(ctx context.Context, collection, key string, doc json.RawMessage) error {
	_, err := t.tx.Exec(ctx,
		`INSERT INTO docs (collection, key, doc) VALUES ($1, $2, $3)
		 ON CONFLICT (collection, key) DO UPDATE SET doc = EXCLUDED.doc`,
		collection, key, []byte(doc))
	if err != nil {
		return fmt.Errorf("store: postgres tx put %s/%s: %w", collection, key, err)
	}
	return nil
}

func (t *pgTx) Get(ctx context.Context, collection, key string) (json.RawMessage, bool, error) {
	var doc []byte
	err := t.tx.QueryRow(ctx, `SELECT doc FROM docs WHERE collection = $1 AND key = $2`, collection, key).Scan(&doc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: postgres tx get %s/%s: %w", collection, key, err)
	}
	return json.RawMessage(doc), true, nil
}

func (t *pgTx) List(ctx context.Context, collection string) ([]Record, error) {
	rows, err := t.tx.Query(ctx, `SELECT key, doc FROM docs WHERE collection = $1 ORDER BY key`, collection)
	if err != nil {
		return nil, fmt.Errorf("store: postgres tx list %s: %w", collection, err)
	}
	return scanPGRecords(rows, collection)
}

func (t *pgTx) Delete(ctx context.Context, collection, key string) error {
	_, err := t.tx.Exec(ctx, `DELETE FROM docs WHERE collection = $1 AND key = $2`, collection, key)
	if err != nil {
		return fmt.Errorf("store: postgres tx delete %s/%s: %w", collection, key, err)
	}
	return nil
}

func (t *pgTx) Reset(ctx context.Context, collection string) error {
	_, err := t.tx.Exec(ctx, `DELETE FROM docs WHERE collection = $1`, collection)
	if err != nil {
		return fmt.Errorf("store: postgres tx reset %s: %w", collection, err)
	}
	return nil
}

func (t *pgTx) Commit() error   { return t.tx.Commit(t.ctx) }
func (t *pgTx) Rollback() error { return t.tx.Rollback(t.ctx) }

// Close is a no-op on a tx (Commit/Rollback end it); it exists to satisfy Store.
func (t *pgTx) Close() error { return nil }
