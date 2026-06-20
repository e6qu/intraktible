// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// GetDoc loads collection[key] and JSON-decodes it into T. It is the typed
// accessor read-model projectors use instead of hand-rolling Get + Unmarshal.
func GetDoc[T any](ctx context.Context, s Store, collection, key string) (T, bool, error) {
	var v T
	doc, ok, err := s.Get(ctx, collection, key)
	if err != nil || !ok {
		return v, ok, err
	}
	if err := json.Unmarshal(doc, &v); err != nil {
		return v, false, fmt.Errorf("store: decode %s/%s: %w", collection, key, err)
	}
	return v, true, nil
}

// UpdateDoc loads collection[key] as T, applies mutate, and writes it back. It
// returns false (and does not write) when the key is absent — projectors use this
// to fail loudly on an event for an aggregate that should already exist.
//
// The read-modify-write is atomic when the backend supports it: a transactional
// store (SQLite, Postgres) runs it inside a single transaction so a concurrent
// writer can't interleave between the read and the write. A caller already inside
// a transaction (a Tx, which is a Store but not a TxStore) takes the direct path —
// the outer transaction is its atomicity boundary — as does the single-writer
// in-memory store.
func UpdateDoc[T any](ctx context.Context, s Store, collection, key string, mutate func(*T)) (bool, error) {
	if _, inTx := s.(Tx); inTx {
		return updateDocDirect(ctx, s, collection, key, mutate)
	}
	txs, ok := s.(TxStore)
	if !ok {
		return updateDocDirect(ctx, s, collection, key, mutate)
	}
	tx, err := txs.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("store: update %s/%s begin: %w", collection, key, err)
	}
	applied, err := updateDocDirect(ctx, tx, collection, key, mutate)
	if err != nil || !applied {
		_ = tx.Rollback()
		return applied, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: update %s/%s commit: %w", collection, key, err)
	}
	return true, nil
}

func updateDocDirect[T any](ctx context.Context, s Store, collection, key string, mutate func(*T)) (bool, error) {
	v, ok, err := GetDoc[T](ctx, s, collection, key)
	if err != nil || !ok {
		return ok, err
	}
	mutate(&v)
	return true, PutDoc(ctx, s, collection, key, v)
}

// PutDoc JSON-encodes v and stores it at collection[key].
func PutDoc[T any](ctx context.Context, s Store, collection, key string, v T) error {
	doc, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("store: encode %s/%s: %w", collection, key, err)
	}
	return s.Put(ctx, collection, key, doc)
}

// ListDocs returns every document in collection whose key has the given prefix,
// JSON-decoded, in store order (used to scope a collection to one tenant).
func ListDocs[T any](ctx context.Context, s Store, collection, prefix string) ([]T, error) {
	recs, err := s.List(ctx, collection, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(recs))
	for _, rec := range recs {
		// Backstop: the store already range-scans by prefix, but re-filter here so
		// correctness never depends on the backend's range-bound arithmetic.
		if !strings.HasPrefix(rec.Key, prefix) {
			continue
		}
		var v T
		if err := json.Unmarshal(rec.Doc, &v); err != nil {
			return nil, fmt.Errorf("store: decode %s/%s: %w", collection, rec.Key, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// QueryDocs lists the prefix-scoped documents of collection, keeps those matching
// keep, and sorts the survivors by less — the shared shape of a filtered read-model
// listing. A nil keep keeps all; a nil less leaves store order.
func QueryDocs[T any](ctx context.Context, s Store, collection, prefix string, keep func(T) bool, less func(a, b T) bool) ([]T, error) {
	all, err := ListDocs[T](ctx, s, collection, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(all))
	for _, v := range all {
		if keep == nil || keep(v) {
			out = append(out, v)
		}
	}
	if less != nil {
		sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	}
	return out, nil
}
