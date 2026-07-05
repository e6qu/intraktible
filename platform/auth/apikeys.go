// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"sync"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

const managedKeyCollection = "auth_managed_keys"

// managedKeyIndexCollection maps a secret's hash to its key id, so ResolveSecret
// (on the auth hot path) is a keyed lookup instead of a full cross-tenant scan —
// which an attacker could otherwise amplify by spamming bogus keys. It is global
// (not tenant-scoped): the secret hash is the credential, and the tenant comes
// from the resolved key. Operational state, never a projection.
const managedKeyIndexCollection = "auth_managed_key_index"

type keyIndexEntry struct {
	KeyID string `json:"key_id"`
}

// Audit stream + event types for managed-token lifecycle. They are appended to
// the event log by the HTTP shell so the immutable audit surface records who
// created or revoked which token — the token store itself is operational state,
// not a projection.
const (
	AuditStream            = "auth"
	EventManagedKeyCreated = "auth.managed_key.created"
	EventManagedKeyRevoked = "auth.managed_key.revoked"
	EventManagedKeyRotated = "auth.managed_key.rotated"
)

// APIKeyAudit is the audit-log payload for a token lifecycle event. KeyID is
// surfaced so the audit trail can be filtered to one token (?resource=). It
// never carries the secret or its hash.
type APIKeyAudit struct {
	KeyID      string     `json:"key_id"`
	Name       string     `json:"name"`
	Role       Role       `json:"role,omitempty"`
	Scope      Scope      `json:"scope,omitempty"`
	TokenActor string     `json:"token_actor,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// ManagedAPIKey is the durable metadata for an operator-managed API token. The
// secret itself is generated once and only its SHA-256 hash is stored.
type ManagedAPIKey struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Identity  identity.Identity `json:"identity"`
	Scope     Scope             `json:"scope"`
	Role      Role              `json:"role"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
	RevokedAt *time.Time        `json:"revoked_at,omitempty"`
	RotatedAt *time.Time        `json:"rotated_at,omitempty"`
	Hash      string            `json:"hash,omitempty"`
	// PrevHash is the prior secret's hash, honored until PrevHashExpiresAt so a
	// rotation can roll out with no downtime. Both are cleared on read.
	PrevHash          string     `json:"prev_hash,omitempty"`
	PrevHashExpiresAt *time.Time `json:"prev_hash_expires_at,omitempty"`
}

// KeyStatus is a managed token's lifecycle state, derived from its timestamps as
// of a given instant. It exists so "is this key usable" is computed in ONE place:
// the usability check was previously open-coded differently in validate (revoked
// OR expired), Rotate (revoked only), and the UI — a divergence where a future
// caller could forget the expiry check and reopen an auth-bypass. Status/Usable
// make that a single typed predicate.
type KeyStatus string

const (
	KeyActive  KeyStatus = "active"
	KeyExpired KeyStatus = "expired"
	KeyRevoked KeyStatus = "revoked"
)

// Status reports the token's lifecycle state as of now. Revocation wins over
// expiry (a revoked key is revoked regardless of its expiry).
func (k ManagedAPIKey) Status(now time.Time) KeyStatus {
	switch {
	case k.RevokedAt != nil:
		return KeyRevoked
	case k.ExpiresAt != nil && now.After(*k.ExpiresAt):
		return KeyExpired
	default:
		return KeyActive
	}
}

// Usable reports whether the token may authenticate a request as of now — the
// single authority for "this key is live" (a non-active key, for any reason,
// authenticates nothing).
func (k ManagedAPIKey) Usable(now time.Time) bool { return k.Status(now) == KeyActive }

// StoreAPIKeys persists managed API tokens in store.Store. It is operational
// state, like sessions, not a rebuildable projection.
type StoreAPIKeys struct {
	store store.Store
	now   func() time.Time

	// One-time backfill of the hash index from keys created before the index
	// existed; new keys are indexed at Create/Rotate. Guarded so the migration
	// scan runs at most once per process.
	mu         sync.Mutex
	backfilled bool
}

// NewStoreAPIKeys builds a store-backed managed-token registry.
func NewStoreAPIKeys(s store.Store) *StoreAPIKeys {
	return &StoreAPIKeys{store: s, now: time.Now}
}

// WithNow overrides the clock key lifecycle timestamps read (deterministic
// tests, the demo seeder) and returns the registry.
func (s *StoreAPIKeys) WithNow(now func() time.Time) *StoreAPIKeys {
	s.now = now
	return s
}

// Create records a generated API key and returns its metadata plus the one-time
// visible secret.
func (s *StoreAPIKeys) Create(ctx context.Context, key ManagedAPIKey) (ManagedAPIKey, string, error) {
	if key.ID == "" {
		key.ID = newToken()
	}
	if _, ok, err := store.GetDoc[ManagedAPIKey](ctx, s.store, managedKeyCollection, key.ID); err != nil {
		return ManagedAPIKey{}, "", err
	} else if ok {
		return ManagedAPIKey{}, "", fmt.Errorf("auth: api key %q already exists", key.ID)
	}
	if key.Name == "" {
		return ManagedAPIKey{}, "", fmt.Errorf("auth: api key name is required")
	}
	if err := key.Identity.Valid(); err != nil {
		return ManagedAPIKey{}, "", err
	}
	if key.Scope == "" {
		key.Scope = Sandbox
	}
	if !ValidScope(key.Scope) {
		return ManagedAPIKey{}, "", fmt.Errorf("auth: invalid api key scope %q", key.Scope)
	}
	if key.Role.Rank() == 0 {
		return ManagedAPIKey{}, "", fmt.Errorf("auth: invalid api key role %q", key.Role)
	}
	now := s.now().UTC()
	if key.ExpiresAt != nil && !key.ExpiresAt.After(now) {
		return ManagedAPIKey{}, "", fmt.Errorf("auth: api key expires_at must be in the future")
	}
	secret := "itk_" + newToken()
	key.CreatedAt = now
	key.Hash = hash(secret)
	if err := store.PutDoc(ctx, s.store, managedKeyCollection, key.ID, key); err != nil {
		return ManagedAPIKey{}, "", err
	}
	if err := s.indexHash(ctx, key.Hash, key.ID); err != nil {
		return ManagedAPIKey{}, "", err
	}
	return redactManagedKey(key), secret, nil
}

// indexHash records hash -> key id for O(1) resolution.
func (s *StoreAPIKeys) indexHash(ctx context.Context, h, keyID string) error {
	return store.PutDoc(ctx, s.store, managedKeyIndexCollection, h, keyIndexEntry{KeyID: keyID})
}

// deindexHash drops a hash's index row once it can no longer authenticate (a
// superseded rotation hash or a revoked key's hashes), so the global index does
// not accumulate orphaned rows for every rotation/revocation over a key's life.
// Best-effort: the durable key doc and any new index row are already written, and
// a stale row never authenticates (validate gates on Usable + secretMatches), so a
// failure to prune is hygiene, not correctness — never block the operation on it.
func (s *StoreAPIKeys) deindexHash(ctx context.Context, h string) {
	if h == "" {
		return
	}
	_ = s.store.Delete(ctx, managedKeyIndexCollection, h)
}

// List returns active and revoked managed tokens, with hashes omitted.
func (s *StoreAPIKeys) List(ctx context.Context) ([]ManagedAPIKey, error) {
	recs, err := store.ListDocs[ManagedAPIKey](ctx, s.store, managedKeyCollection, "")
	if err != nil {
		return nil, err
	}
	for i := range recs {
		recs[i] = redactManagedKey(recs[i])
	}
	return recs, nil
}

// Get loads one managed token by id, with the secret hash omitted.
func (s *StoreAPIKeys) Get(ctx context.Context, id string) (ManagedAPIKey, bool, error) {
	key, ok, err := store.GetDoc[ManagedAPIKey](ctx, s.store, managedKeyCollection, id)
	if err != nil || !ok {
		return ManagedAPIKey{}, ok, err
	}
	return redactManagedKey(key), true, nil
}

// Revoke marks a token unusable. Unknown ids fail loudly.
func (s *StoreAPIKeys) Revoke(ctx context.Context, id string) (ManagedAPIKey, error) {
	key, ok, err := store.GetDoc[ManagedAPIKey](ctx, s.store, managedKeyCollection, id)
	if err != nil {
		return ManagedAPIKey{}, err
	}
	if !ok {
		return ManagedAPIKey{}, fmt.Errorf("auth: api key %q not found", id)
	}
	now := s.now().UTC()
	key.RevokedAt = &now
	if err := store.PutDoc(ctx, s.store, managedKeyCollection, id, key); err != nil {
		return ManagedAPIKey{}, err
	}
	// A revoked key authenticates nothing, so its index rows are dead — prune both
	// the current and any in-grace previous hash rather than leaving them orphaned.
	s.deindexHash(ctx, key.Hash)
	s.deindexHash(ctx, key.PrevHash)
	return redactManagedKey(key), nil
}

// Rotate issues a fresh secret for an existing token, returning it once. The
// previous secret keeps working until now+grace (grace 0 = effective
// immediately), so an operator can roll the new secret out with no downtime.
func (s *StoreAPIKeys) Rotate(ctx context.Context, id string, grace time.Duration) (ManagedAPIKey, string, error) {
	key, ok, err := store.GetDoc[ManagedAPIKey](ctx, s.store, managedKeyCollection, id)
	if err != nil {
		return ManagedAPIKey{}, "", err
	}
	if !ok {
		return ManagedAPIKey{}, "", fmt.Errorf("auth: api key %q not found", id)
	}
	if key.Status(s.now().UTC()) == KeyRevoked {
		return ManagedAPIKey{}, "", fmt.Errorf("auth: api key %q is revoked", id)
	}
	now := s.now().UTC()
	secret := "itk_" + newToken()
	// Capture the hashes this rotation supersedes so their index rows can be pruned:
	// the prior in-grace previous hash always retires now, and the current hash too
	// unless this rotation keeps it as the new grace-window previous.
	retiredPrev := key.PrevHash
	priorHash := key.Hash
	if grace > 0 {
		exp := now.Add(grace)
		key.PrevHash = key.Hash
		key.PrevHashExpiresAt = &exp
	} else {
		key.PrevHash = ""
		key.PrevHashExpiresAt = nil
	}
	key.Hash = hash(secret)
	key.RotatedAt = &now
	if err := store.PutDoc(ctx, s.store, managedKeyCollection, id, key); err != nil {
		return ManagedAPIKey{}, "", err
	}
	// Index the new hash. The previous hash's index entry (written at the original
	// Create/Rotate) stays valid through the grace window — secretMatches gates it.
	if err := s.indexHash(ctx, key.Hash, id); err != nil {
		return ManagedAPIKey{}, "", err
	}
	// Prune the index rows this rotation retired: the superseded previous hash, and
	// (when no grace window keeps it) the just-rotated current hash. Guard against
	// dropping a row we still rely on — the new current hash or the kept previous.
	for _, h := range []string{retiredPrev, priorHash} {
		if h != "" && h != key.Hash && h != key.PrevHash {
			s.deindexHash(ctx, h)
		}
	}
	return redactManagedKey(key), secret, nil
}

// ResolveSecret implements KeyResolver for Authenticate. It resolves via the hash
// index (O(1)) rather than scanning every key. A miss is authoritative once the
// one-time backfill of pre-index keys has run — so a flood of bogus keys can't
// amplify into repeated full scans — falling back to a scan only if that backfill
// could not complete (a store error), so a valid key is never wrongly denied.
func (s *StoreAPIKeys) ResolveSecret(secret string) (APIKey, bool) {
	ctx := context.Background()
	want := hash(secret)
	if key, ok, resolved := s.resolveViaIndex(ctx, want); resolved {
		return key, ok
	}
	if s.ensureIndexed(ctx) {
		key, ok, _ := s.resolveViaIndex(ctx, want)
		return key, ok // index is complete: a miss is a real miss, no scan
	}
	return s.resolveViaScan(ctx, want) // backfill unavailable — degrade to a scan
}

// resolveViaIndex looks the hash up in the index. resolved=false means the hash
// is not indexed (the caller may backfill + retry, or scan); resolved=true with
// ok=false means the hash mapped to a key that is revoked/expired/non-matching.
func (s *StoreAPIKeys) resolveViaIndex(ctx context.Context, want string) (key APIKey, ok, resolved bool) {
	entry, found, err := store.GetDoc[keyIndexEntry](ctx, s.store, managedKeyIndexCollection, want)
	if err != nil || !found {
		return APIKey{}, false, false
	}
	rec, found, err := store.GetDoc[ManagedAPIKey](ctx, s.store, managedKeyCollection, entry.KeyID)
	if err != nil || !found {
		return APIKey{}, false, true
	}
	if valid, k := s.validate(rec, want); valid {
		return k, true, true
	}
	return APIKey{}, false, true
}

func (s *StoreAPIKeys) resolveViaScan(ctx context.Context, want string) (APIKey, bool) {
	recs, err := store.ListDocs[ManagedAPIKey](ctx, s.store, managedKeyCollection, "")
	if err != nil {
		return APIKey{}, false
	}
	for _, rec := range recs {
		if valid, k := s.validate(rec, want); valid {
			return k, true
		}
	}
	return APIKey{}, false
}

func (s *StoreAPIKeys) validate(rec ManagedAPIKey, want string) (bool, APIKey) {
	now := s.now()
	if !rec.Usable(now) {
		return false, APIKey{}
	}
	if !secretMatches(rec, want, now) {
		return false, APIKey{}
	}
	return true, APIKey{ID: rec.ID, Identity: rec.Identity, Scope: rec.Scope, Role: rec.Role}
}

// ensureIndexed backfills the hash index from any keys created before the index
// existed, at most once per process. Returns whether the index can be trusted as
// complete (false on a store error, so the caller falls back to a scan).
func (s *StoreAPIKeys) ensureIndexed(ctx context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backfilled {
		return true
	}
	recs, err := store.ListDocs[ManagedAPIKey](ctx, s.store, managedKeyCollection, "")
	if err != nil {
		return false
	}
	for _, rec := range recs {
		if rec.Hash != "" {
			if err := s.indexHash(ctx, rec.Hash, rec.ID); err != nil {
				return false
			}
		}
		if rec.PrevHash != "" {
			if err := s.indexHash(ctx, rec.PrevHash, rec.ID); err != nil {
				return false
			}
		}
	}
	s.backfilled = true
	return true
}

// secretMatches accepts the current hash, or the previous hash while its
// rotation grace window is still open.
func secretMatches(rec ManagedAPIKey, want string, now time.Time) bool {
	if subtle.ConstantTimeCompare([]byte(rec.Hash), []byte(want)) == 1 {
		return true
	}
	return rec.PrevHash != "" && rec.PrevHashExpiresAt != nil && now.Before(*rec.PrevHashExpiresAt) &&
		subtle.ConstantTimeCompare([]byte(rec.PrevHash), []byte(want)) == 1
}

func redactManagedKey(key ManagedAPIKey) ManagedAPIKey {
	key.Hash = ""
	key.PrevHash = ""
	return key
}
