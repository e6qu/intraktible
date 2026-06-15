// SPDX-License-Identifier: AGPL-3.0-or-later

// Package store is the read-side projection store: a collection/key/JSON
// document KV. Projections are rebuildable from the event log,
// so the store is a derived view, not the source of truth. The interface is
// pluggable (in-memory now; SQLite/Postgres JSONB adapters next).
package store

import (
	"context"
	"encoding/json"
)

// Record is a stored document with its key.
type Record struct {
	Key string          `json:"key"`
	Doc json.RawMessage `json:"doc"`
}

// Store is a pluggable JSON document KV grouped into collections.
// Implementations must be safe for concurrent use.
type Store interface {
	Put(ctx context.Context, collection, key string, doc json.RawMessage) error
	Get(ctx context.Context, collection, key string) (json.RawMessage, bool, error)
	List(ctx context.Context, collection string) ([]Record, error)
	Delete(ctx context.Context, collection, key string) error
	// Reset clears a collection (used when a projection rebuilds from offset 0).
	Reset(ctx context.Context, collection string) error
	Close() error
}

// Tx is a Store scoped to a transaction: its writes are atomic — they all commit
// together or roll back together. It satisfies Store, so a projector can write
// through it unchanged. The projection runtime uses a Tx to apply one event AND
// advance its checkpoint atomically, which makes crash-safe incremental resume
// possible without requiring every projector to be idempotent.
type Tx interface {
	Store
	Commit() error
	Rollback() error
}

// TxStore is a Store whose writes can be grouped into an atomic transaction —
// the durable backends (SQLite, Postgres). A Store that does not implement it
// (the ephemeral in-memory store) is always fully rebuilt from the log at boot.
type TxStore interface {
	Store
	Begin(ctx context.Context) (Tx, error)
}

// Key namespaces a document by tenant so collections stay per-(org,workspace).
func Key(org, workspace, id string) string {
	return org + "/" + workspace + "/" + id
}
