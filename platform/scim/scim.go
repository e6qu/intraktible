// SPDX-License-Identifier: AGPL-3.0-or-later

// Package scim is a minimal SCIM 2.0 Users provisioning surface: the companion
// to OIDC SSO. An IdP (Okta, Azure AD, …) creates/updates/deactivates users
// here, and the OIDC login consults it so a user deactivated in the IdP can no
// longer obtain a session. Users are operational state (like sessions and API
// keys), stored directly — not an event-sourced projection.
package scim

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/store"
)

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

const collection = "scim_users"

// UserSchema is the SCIM core user schema URN.
const UserSchema = "urn:ietf:params:scim:schemas:core:2.0:User"

// User is the subset of the SCIM core User resource we persist and serve.
type User struct {
	Schemas    []string `json:"schemas,omitempty"`
	ID         string   `json:"id"`
	ExternalID string   `json:"externalId,omitempty"`
	UserName   string   `json:"userName"`
	Active     bool     `json:"active"`
	Meta       *Meta    `json:"meta,omitempty"`
	// Tenant scoping — set by the service from the SCIM token, not the wire body.
	Org       string `json:"-"`
	Workspace string `json:"-"`
}

// Meta is the SCIM resource metadata.
type Meta struct {
	ResourceType string    `json:"resourceType"`
	Created      time.Time `json:"created"`
	LastModified time.Time `json:"lastModified"`
}

// Store persists SCIM users in store.Store, scoped per (org, workspace).
type Store struct {
	store store.Store
	now   func() time.Time
}

// NewStore builds a store-backed SCIM user registry.
func NewStore(s store.Store) *Store {
	return &Store{store: s, now: time.Now}
}

func key(org, workspace, id string) string { return store.Key(org, workspace, id) }

// Create provisions a new user. userName is required and unique per tenant.
func (s *Store) Create(ctx context.Context, u User) (User, error) {
	u.UserName = strings.TrimSpace(u.UserName)
	if u.UserName == "" {
		return User{}, fmt.Errorf("scim: userName is required")
	}
	if existing, ok, err := s.byUserName(ctx, u.Org, u.Workspace, u.UserName); err != nil {
		return User{}, err
	} else if ok {
		return User{}, fmt.Errorf("scim: user %q already exists (id %s)", u.UserName, existing.ID)
	}
	if u.ID == "" {
		u.ID = newID()
	}
	now := s.now().UTC()
	u.Schemas = []string{UserSchema}
	u.Meta = &Meta{ResourceType: "User", Created: now, LastModified: now}
	if err := store.PutDoc(ctx, s.store, collection, key(u.Org, u.Workspace, u.ID), u); err != nil {
		return User{}, err
	}
	return u, nil
}

// Get loads one user by id.
func (s *Store) Get(ctx context.Context, org, workspace, id string) (User, bool, error) {
	return store.GetDoc[User](ctx, s.store, collection, key(org, workspace, id))
}

// List returns the tenant's users, optionally filtered to a single userName
// (the only SCIM filter IdPs need to look up an existing user before creating).
func (s *Store) List(ctx context.Context, org, workspace, userNameFilter string) ([]User, error) {
	all, err := store.ListDocs[User](ctx, s.store, collection, store.Key(org, workspace, ""))
	if err != nil {
		return nil, err
	}
	if userNameFilter == "" {
		return all, nil
	}
	out := make([]User, 0, 1)
	for _, u := range all {
		if strings.EqualFold(u.UserName, userNameFilter) {
			out = append(out, u)
		}
	}
	return out, nil
}

// SetActive flips a user's active flag — the deprovision/reactivate operation.
func (s *Store) SetActive(ctx context.Context, org, workspace, id string, active bool) (User, error) {
	u, ok, err := s.Get(ctx, org, workspace, id)
	if err != nil {
		return User{}, err
	}
	if !ok {
		return User{}, fmt.Errorf("scim: user %q not found", id)
	}
	u.Active = active
	if u.Meta != nil {
		u.Meta.LastModified = s.now().UTC()
	}
	if err := store.PutDoc(ctx, s.store, collection, key(org, workspace, id), u); err != nil {
		return User{}, err
	}
	return u, nil
}

// Delete removes a user.
func (s *Store) Delete(ctx context.Context, org, workspace, id string) error {
	return s.store.Delete(ctx, collection, key(org, workspace, id))
}

// Allowed reports whether a login for email may proceed: it blocks only a user
// that exists and is inactive. An unprovisioned user is allowed (SCIM gates
// deprovisioning, not first login), so enabling SCIM never locks out new users.
func (s *Store) Allowed(ctx context.Context, org, workspace, email string) bool {
	u, ok, err := s.byUserName(ctx, org, workspace, email)
	if err != nil || !ok {
		return true
	}
	return u.Active
}

func (s *Store) byUserName(ctx context.Context, org, workspace, userName string) (User, bool, error) {
	users, err := s.List(ctx, org, workspace, userName)
	if err != nil {
		return User{}, false, err
	}
	if len(users) == 0 {
		return User{}, false, nil
	}
	return users[0], true, nil
}
