// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"encoding/json"
	"fmt"
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
	recs, err := s.List(ctx, collection)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(recs))
	for _, rec := range recs {
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
