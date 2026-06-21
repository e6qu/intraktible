// SPDX-License-Identifier: AGPL-3.0-or-later

package store_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/e6qu/intraktible/platform/secretbox"
	"github.com/e6qu/intraktible/platform/store"
)

func testKeyring(t *testing.T) *secretbox.Keyring {
	t.Helper()
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	kr, err := secretbox.NewKeyring(k)
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

// Documents are sealed at rest: the inner store holds a ciphertext envelope (not the
// plaintext), while reads through the wrapper return the original plaintext.
func TestEncryptedStoreSealsAtRest(t *testing.T) {
	ctx := context.Background()
	inner := store.NewMemory()
	enc := store.Encrypted(inner, testKeyring(t))

	if err := enc.Put(ctx, "flows", "k1", json.RawMessage(`{"slug":"kyc"}`)); err != nil {
		t.Fatal(err)
	}

	// The inner store must NOT hold the plaintext.
	raw, ok, err := inner.Get(ctx, "flows", "k1")
	if err != nil || !ok {
		t.Fatal("inner get", err)
	}
	if string(raw) == `{"slug":"kyc"}` {
		t.Fatal("plaintext stored at rest")
	}
	if !secretbox.IsSealed(raw) {
		t.Fatalf("inner doc should be a sealed envelope, got %s", raw)
	}

	// The wrapper returns the plaintext.
	got, ok, err := enc.Get(ctx, "flows", "k1")
	if err != nil || !ok || string(got) != `{"slug":"kyc"}` {
		t.Fatalf("wrapper get = %q, %v", got, err)
	}
}

// A plaintext doc written before encryption was enabled still reads through the
// wrapper (transparent migration — no re-encrypt pass needed).
func TestEncryptedStorePlaintextPassthrough(t *testing.T) {
	ctx := context.Background()
	inner := store.NewMemory()
	if err := inner.Put(ctx, "c", "legacy", json.RawMessage(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	enc := store.Encrypted(inner, testKeyring(t))
	got, ok, err := enc.Get(ctx, "c", "legacy")
	if err != nil || !ok || string(got) != `{"v":1}` {
		t.Fatalf("legacy plaintext read = %q, %v", got, err)
	}
	// List also opens/passes through mixed rows.
	_ = enc.Put(ctx, "c", "new", json.RawMessage(`{"v":2}`))
	recs, err := enc.List(ctx, "c", "")
	if err != nil || len(recs) != 2 {
		t.Fatalf("list = %d recs, %v", len(recs), err)
	}
	for _, r := range recs {
		if secretbox.IsSealed(r.Doc) {
			t.Fatalf("List should return plaintext, got sealed for %s", r.Key)
		}
	}
}

// A nil keyring leaves the store unwrapped (encryption disabled).
func TestEncryptedStoreNilKeyringPassthrough(t *testing.T) {
	inner := store.NewMemory()
	if got := store.Encrypted(inner, nil); got != store.Store(inner) {
		t.Fatal("nil keyring should return the inner store unchanged")
	}
}

// The TxStore + UpdateDoc (read-modify-write) path survives the wrapper on a durable
// backend, and the committed value is sealed at rest.
func TestEncryptedStoreTxUpdate(t *testing.T) {
	ctx := context.Background()
	inner, err := store.NewSQLite(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inner.Close() }()
	enc := store.Encrypted(inner, testKeyring(t))

	type doc struct {
		N int `json:"n"`
	}
	if err := store.PutDoc(ctx, enc, "c", "k", doc{N: 1}); err != nil {
		t.Fatal(err)
	}
	ok, err := store.UpdateDoc(ctx, enc, "c", "k", func(d *doc) { d.N += 41 })
	if err != nil || !ok {
		t.Fatalf("update: ok=%v err=%v", ok, err)
	}
	got, _, err := store.GetDoc[doc](ctx, enc, "c", "k")
	if err != nil || got.N != 42 {
		t.Fatalf("after update n=%d, %v", got.N, err)
	}
	// At rest in SQLite, the doc is sealed.
	raw, _, _ := inner.Get(ctx, "c", "k")
	if !secretbox.IsSealed(raw) {
		t.Fatalf("durable doc should be sealed: %s", raw)
	}
}
