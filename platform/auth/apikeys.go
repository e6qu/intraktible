// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

const managedKeyCollection = "auth_managed_keys"

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

// StoreAPIKeys persists managed API tokens in store.Store. It is operational
// state, like sessions, not a rebuildable projection.
type StoreAPIKeys struct {
	store store.Store
	now   func() time.Time
}

// NewStoreAPIKeys builds a store-backed managed-token registry.
func NewStoreAPIKeys(s store.Store) *StoreAPIKeys {
	return &StoreAPIKeys{store: s, now: time.Now}
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
	if key.Scope != Sandbox && key.Scope != Production {
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
	return redactManagedKey(key), secret, nil
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
	if key.RevokedAt != nil {
		return ManagedAPIKey{}, "", fmt.Errorf("auth: api key %q is revoked", id)
	}
	now := s.now().UTC()
	secret := "itk_" + newToken()
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
	return redactManagedKey(key), secret, nil
}

// ResolveSecret implements KeyResolver for Authenticate.
func (s *StoreAPIKeys) ResolveSecret(secret string) (APIKey, bool) {
	want := hash(secret)
	recs, err := store.ListDocs[ManagedAPIKey](context.Background(), s.store, managedKeyCollection, "")
	if err != nil {
		return APIKey{}, false
	}
	now := s.now()
	for _, rec := range recs {
		if rec.RevokedAt != nil || (rec.ExpiresAt != nil && now.After(*rec.ExpiresAt)) {
			continue
		}
		if !secretMatches(rec, want, now) {
			continue
		}
		return APIKey{ID: rec.ID, Identity: rec.Identity, Scope: rec.Scope, Role: rec.Role}, true
	}
	return APIKey{}, false
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
