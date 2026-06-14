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

// Key namespaces a document by tenant so collections stay per-(org,workspace).
func Key(org, workspace, id string) string {
	return org + "/" + workspace + "/" + id
}
