// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"encoding/json"

	"github.com/e6qu/intraktible/platform/secretbox"
)

// Encrypted wraps a Store so document VALUES are sealed at rest under the keyring
// (AES-256-GCM via platform/secretbox). Collection names and keys stay plaintext —
// the backends key, range-scan, and index on them — so no query, prefix scan, or
// index is affected; only the document bytes become a sealed JSON envelope. Because
// the envelope is recognizable (secretbox.IsSealed), reads transparently open sealed
// docs and pass any plaintext doc through unchanged: enabling encryption needs no
// migration pass — pre-existing rows read fine and new writes seal. A nil keyring
// returns the store unwrapped (encryption disabled).
//
// The wrapper preserves the inner store's capabilities — TxStore (atomic
// projection apply) or Ephemeral (the in-memory store) — by returning the matching
// concrete type, and always offers a row-locking read that falls back to a plain
// read when the backend has none.
func Encrypted(inner Store, kr *secretbox.Keyring) Store {
	if kr == nil {
		return inner
	}
	base := &encStore{inner: inner, kr: kr}
	switch inner.(type) {
	case TxStore:
		return &encTxStore{encStore: base}
	case Ephemeral:
		return &encEphemeralStore{encStore: base}
	default:
		return base
	}
}

// encStore is the shared Store implementation: seal on write, open on read.
type encStore struct {
	inner Store
	kr    *secretbox.Keyring
}

// seal/open pass a nil aad: a stored document's value is not bound to a specific
// location the way a connector credential is, so it needs no AAD binding.
func (e *encStore) seal(doc json.RawMessage) (json.RawMessage, error) {
	return e.kr.Seal(doc, nil)
}

// open returns the plaintext of a stored doc: a sealed envelope is opened, while a
// plaintext doc (written before encryption was enabled) passes straight through.
func (e *encStore) open(doc json.RawMessage) (json.RawMessage, error) {
	if !secretbox.IsSealed(doc) {
		return doc, nil
	}
	return e.kr.Open(doc, nil)
}

func (e *encStore) Put(ctx context.Context, collection, key string, doc json.RawMessage) error {
	sealed, err := e.seal(doc)
	if err != nil {
		return err
	}
	return e.inner.Put(ctx, collection, key, sealed)
}

func (e *encStore) Get(ctx context.Context, collection, key string) (json.RawMessage, bool, error) {
	doc, ok, err := e.inner.Get(ctx, collection, key)
	if err != nil || !ok {
		return nil, ok, err
	}
	plain, err := e.open(doc)
	return plain, true, err
}

// GetForUpdate satisfies the optional row-lock capability uniformly: it row-locks
// when the backend offers one (Postgres) and otherwise reads plainly, decrypting in
// both cases. This means the read-modify-write lock survives the wrapper.
func (e *encStore) GetForUpdate(ctx context.Context, collection, key string) (json.RawMessage, bool, error) {
	var (
		doc json.RawMessage
		ok  bool
		err error
	)
	if rl, isLocker := e.inner.(rowLocker); isLocker {
		doc, ok, err = rl.GetForUpdate(ctx, collection, key)
	} else {
		doc, ok, err = e.inner.Get(ctx, collection, key)
	}
	if err != nil || !ok {
		return nil, ok, err
	}
	plain, err := e.open(doc)
	return plain, true, err
}

func (e *encStore) List(ctx context.Context, collection, keyPrefix string) ([]Record, error) {
	recs, err := e.inner.List(ctx, collection, keyPrefix)
	if err != nil {
		return nil, err
	}
	for i := range recs {
		plain, err := e.open(recs[i].Doc)
		if err != nil {
			return nil, err
		}
		recs[i].Doc = plain
	}
	return recs, nil
}

func (e *encStore) Delete(ctx context.Context, collection, key string) error {
	return e.inner.Delete(ctx, collection, key)
}

func (e *encStore) Reset(ctx context.Context, collection string) error {
	return e.inner.Reset(ctx, collection)
}

func (e *encStore) Close() error { return e.inner.Close() }

// encTxStore adds the atomic-transaction capability, wrapping each inner Tx so its
// writes seal and reads open like the top-level store.
type encTxStore struct {
	*encStore
}

func (e *encTxStore) Begin(ctx context.Context) (Tx, error) {
	tx, err := e.inner.(TxStore).Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &encTx{encStore: &encStore{inner: tx, kr: e.kr}, tx: tx}, nil
}

// encTx is the transactional wrapper: its Store methods (from encStore) operate on
// the inner Tx, and Commit/Rollback delegate to it.
type encTx struct {
	*encStore
	tx Tx
}

func (e *encTx) Commit() error   { return e.tx.Commit() }
func (e *encTx) Rollback() error { return e.tx.Rollback() }

// PutIfAbsent seals the doc and delegates to the inner tx (GetForUpdate is inherited
// from the embedded encStore, which row-locks the inner tx and decrypts).
func (e *encTx) PutIfAbsent(ctx context.Context, collection, key string, doc json.RawMessage) error {
	sealed, err := e.seal(doc)
	if err != nil {
		return err
	}
	return e.tx.PutIfAbsent(ctx, collection, key, sealed)
}

// encEphemeralStore marks the wrapper ephemeral when the inner store is, so the
// projection runtime keeps treating it as rebuilt-from-the-log on restart.
type encEphemeralStore struct {
	*encStore
}

func (e *encEphemeralStore) Ephemeral() {}
