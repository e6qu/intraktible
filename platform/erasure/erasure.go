// SPDX-License-Identifier: AGPL-3.0-or-later

// Package erasure implements right-to-erasure for an append-only, immutable
// event log via crypto-shredding: each data subject's PII is sealed under a
// per-subject key, and "erasure" destroys the key — the ciphertext in the log
// (and projections) becomes permanently unreadable while the events themselves
// stay intact for audit. It also supports retention by age (auto-erase subjects
// older than a cutoff). Keys are operational state, scoped per (org, workspace).
package erasure

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/secretbox"
	"github.com/e6qu/intraktible/platform/store"
)

const collection = "erasure_subjects"

// ErrErased is returned when sealing or opening data for a subject whose key has
// been destroyed — the subject's data is irrecoverable, by design.
var ErrErased = errors.New("erasure: subject has been erased")

// subject is the stored per-subject record. Key is cleared on erasure (the
// shred); the record is retained as a tombstone for listing and audit.
type subject struct {
	Subject string     `json:"subject"`
	Key     []byte     `json:"key,omitempty"`
	Created time.Time  `json:"created"`
	Erased  *time.Time `json:"erased,omitempty"`
}

// Vault seals/opens subject PII and erases subjects (crypto-shredding).
type Vault struct {
	store store.Store
	now   func() time.Time
	// mu serializes the subject-record writes that must not interleave: first-use
	// key creation (two racing creators must agree on ONE key) and erasure (a key
	// put landing after the tombstone would silently un-erase the subject).
	mu sync.Mutex
}

// NewVault builds a store-backed erasure vault.
func NewVault(s store.Store) *Vault { return &Vault{store: s, now: time.Now} }

// Seal encrypts plain under subject's key (creating the key on first use). It
// fails with ErrErased if the subject has been erased — erased subjects accept
// no new data.
func (v *Vault) Seal(ctx context.Context, id identity.Identity, subj string, plain []byte) ([]byte, error) {
	rec, ok, err := v.load(ctx, id, subj)
	if err != nil {
		return nil, err
	}
	if !ok {
		if rec, err = v.createKey(ctx, id, subj); err != nil {
			return nil, err
		}
	}
	// Re-checked after createKey too: a lost creation race may have surfaced a
	// concurrently-written tombstone, which must refuse new data, not un-erase.
	if rec.Erased != nil {
		return nil, ErrErased
	}
	return seal(rec.Key, plain)
}

// createKey mints a subject's key on first use. The naive read-miss → keygen →
// put is racy: two concurrent first seals would each persist a different key,
// making the loser's envelopes permanently undecryptable. v.mu serializes
// creators in this process, and on a transactional store the re-check + put
// also run in one transaction, so a record committed by another writer wins
// and is returned instead of being overwritten.
func (v *Vault) createKey(ctx context.Context, id identity.Identity, subj string) (subject, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return subject{}, fmt.Errorf("erasure: key gen: %w", err)
	}
	rec := subject{Subject: subj, Key: key, Created: v.now().UTC()}
	txs, transactional := v.store.(store.TxStore)
	if !transactional {
		existing, ok, err := v.load(ctx, id, subj)
		if err != nil || ok {
			return existing, err
		}
		return rec, v.put(ctx, id, rec)
	}
	tx, err := txs.Begin(ctx)
	if err != nil {
		return subject{}, fmt.Errorf("erasure: create key for %q: begin: %w", subj, err)
	}
	k := store.Key(id.Org, id.Workspace, subj)
	existing, ok, err := store.GetDoc[subject](ctx, tx, collection, k)
	if err != nil || ok {
		_ = tx.Rollback()
		return existing, err
	}
	if err := store.PutDoc(ctx, tx, collection, k, rec); err != nil {
		_ = tx.Rollback()
		return subject{}, err
	}
	if err := tx.Commit(); err != nil {
		return subject{}, fmt.Errorf("erasure: create key for %q: commit: %w", subj, err)
	}
	return rec, nil
}

// Open decrypts a value sealed by Seal. A missing key or an erased subject is
// ErrErased — the data can no longer be recovered.
func (v *Vault) Open(ctx context.Context, id identity.Identity, subj string, sealed []byte) ([]byte, error) {
	rec, ok, err := v.load(ctx, id, subj)
	if err != nil {
		return nil, err
	}
	if !ok || rec.Erased != nil || len(rec.Key) == 0 {
		return nil, ErrErased
	}
	return open(rec.Key, sealed)
}

// Erase destroys a subject's key — the irreversible shred. A subject that was
// never sealed is recorded as an (already) erased tombstone, so a pre-emptive
// erasure still blocks any later Seal.
func (v *Vault) Erase(ctx context.Context, id identity.Identity, subj string) error {
	// Hold the creation lock so the shred can't interleave with a first-use Seal
	// (whose key put would otherwise overwrite the tombstone written here).
	v.mu.Lock()
	defer v.mu.Unlock()
	rec, ok, err := v.load(ctx, id, subj)
	if err != nil {
		return err
	}
	now := v.now().UTC()
	if !ok {
		rec = subject{Subject: subj, Created: now}
	}
	rec.Key = nil
	if rec.Erased == nil {
		rec.Erased = &now
	}
	return v.put(ctx, id, rec)
}

// Erased reports whether a subject has been erased.
func (v *Vault) Erased(ctx context.Context, id identity.Identity, subj string) (bool, error) {
	rec, ok, err := v.load(ctx, id, subj)
	if err != nil || !ok {
		return false, err
	}
	return rec.Erased != nil, nil
}

// ListErased returns the subjects that have been erased, for an audit/compliance
// view of fulfilled erasure requests.
func (v *Vault) ListErased(ctx context.Context, id identity.Identity) ([]string, error) {
	recs, err := store.ListDocs[subject](ctx, v.store, collection, store.Key(id.Org, id.Workspace, ""))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range recs {
		if r.Erased != nil {
			out = append(out, r.Subject)
		}
	}
	return out, nil
}

// RetentionSweep erases every not-yet-erased subject whose key predates
// now-maxAge, enforcing a retention limit, and returns how many it erased.
func (v *Vault) RetentionSweep(ctx context.Context, id identity.Identity, maxAge time.Duration) (int, error) {
	recs, err := store.ListDocs[subject](ctx, v.store, collection, store.Key(id.Org, id.Workspace, ""))
	if err != nil {
		return 0, err
	}
	cutoff := v.now().UTC().Add(-maxAge)
	erased := 0
	for _, r := range recs {
		if r.Erased == nil && r.Created.Before(cutoff) {
			if err := v.Erase(ctx, id, r.Subject); err != nil {
				return erased, err
			}
			erased++
		}
	}
	return erased, nil
}

func (v *Vault) load(ctx context.Context, id identity.Identity, subj string) (subject, bool, error) {
	return store.GetDoc[subject](ctx, v.store, collection, store.Key(id.Org, id.Workspace, subj))
}

func (v *Vault) put(ctx context.Context, id identity.Identity, rec subject) error {
	return store.PutDoc(ctx, v.store, collection, store.Key(id.Org, id.Workspace, rec.Subject), rec)
}

// seal/open delegate to the shared AES-256-GCM primitive (the same nonce-prefixed
// construction this package used inline, so already-sealed subject data still
// opens). The per-subject key management stays here; only the crypto is shared.
func seal(key, plain []byte) ([]byte, error) {
	box, err := secretbox.NewAESGCMSecretBox(key)
	if err != nil {
		return nil, err
	}
	return box.Encrypt(plain, nil)
}

func open(key, sealed []byte) ([]byte, error) {
	box, err := secretbox.NewAESGCMSecretBox(key)
	if err != nil {
		return nil, err
	}
	return box.Decrypt(sealed, nil)
}
