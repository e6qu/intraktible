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
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/intraktible/platform/store"
)

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

const collection = "scim_users"
const groupCollection = "scim_groups"

// UserSchema / GroupSchema are the SCIM core resource schema URNs.
const (
	UserSchema  = "urn:ietf:params:scim:schemas:core:2.0:User"
	GroupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"
)

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
	// memberMu serializes group-membership read-modify-write so concurrent
	// SetMembers/PatchMembers calls cannot lose updates (in-process; cross-node
	// atomicity would need store transactions).
	memberMu sync.Mutex
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
// List returns the tenant's users. When filtered is true the result is exactly the
// users whose userName equals filterUserName (an empty filterUserName matches none —
// userNames are non-empty, so a `userName eq ""` clause is a precise empty result,
// NOT "list everyone"). When filtered is false (no filter clause present) it returns
// all users. Results are sorted by id for deterministic output across backends.
func (s *Store) List(ctx context.Context, org, workspace, filterUserName string, filtered bool) ([]User, error) {
	all, err := store.ListDocs[User](ctx, s.store, collection, store.Key(org, workspace, ""))
	if err != nil {
		return nil, err
	}
	out := all
	if filtered {
		out = make([]User, 0, 1)
		for _, u := range all {
			if strings.EqualFold(u.UserName, filterUserName) {
				out = append(out, u)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
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

// Allowed reports whether a login for email may proceed: an unprovisioned user
// is allowed (SCIM gates deprovisioning, not first login, so enabling it never
// locks out new users), an active provisioned user is allowed, and a deactivated
// one is blocked. A lookup error fails CLOSED (deny + log) — a transient store
// fault must never let a deprovisioned user back in.
func (s *Store) Allowed(ctx context.Context, org, workspace, email string) bool {
	u, ok, err := s.byUserName(ctx, org, workspace, email)
	if err != nil {
		slog.Error("scim: deprovisioning gate lookup failed; denying login", "email", email, "err", err)
		return false
	}
	if !ok {
		return true
	}
	return u.Active
}

func (s *Store) byUserName(ctx context.Context, org, workspace, userName string) (User, bool, error) {
	users, err := s.List(ctx, org, workspace, userName, true) // exact lookup by a concrete name
	if err != nil {
		return User{}, false, err
	}
	if len(users) == 0 {
		return User{}, false, nil
	}
	return users[0], true, nil
}

// Member is a SCIM group member reference (value = a user id).
type Member struct {
	Value string `json:"value"`
}

// Group is the subset of the SCIM core Group resource we persist and serve.
type Group struct {
	Schemas     []string `json:"schemas,omitempty"`
	ID          string   `json:"id"`
	DisplayName string   `json:"displayName"`
	Members     []Member `json:"members,omitempty"`
	Meta        *Meta    `json:"meta,omitempty"`
	Org         string   `json:"-"`
	Workspace   string   `json:"-"`
}

// CreateGroup provisions a new group.
func (s *Store) CreateGroup(ctx context.Context, g Group) (Group, error) {
	g.DisplayName = strings.TrimSpace(g.DisplayName)
	if g.DisplayName == "" {
		return Group{}, fmt.Errorf("scim: group displayName is required")
	}
	if g.ID == "" {
		g.ID = newID()
	}
	now := s.now().UTC()
	g.Schemas = []string{GroupSchema}
	g.Meta = &Meta{ResourceType: "Group", Created: now, LastModified: now}
	if err := store.PutDoc(ctx, s.store, groupCollection, key(g.Org, g.Workspace, g.ID), g); err != nil {
		return Group{}, err
	}
	return g, nil
}

// GetGroup loads one group by id.
func (s *Store) GetGroup(ctx context.Context, org, workspace, id string) (Group, bool, error) {
	return store.GetDoc[Group](ctx, s.store, groupCollection, key(org, workspace, id))
}

// ListGroups returns the tenant's groups.
func (s *Store) ListGroups(ctx context.Context, org, workspace string) ([]Group, error) {
	return store.ListDocs[Group](ctx, s.store, groupCollection, store.Key(org, workspace, ""))
}

// SetMembers replaces a group's members (PUT) or adds/removes the given member
// ids. It is one membership op; PatchMembers applies several atomically.
func (s *Store) SetMembers(ctx context.Context, org, workspace, id string, memberIDs []string, mode MemberMode) (Group, error) {
	return s.PatchMembers(ctx, org, workspace, id, []MemberOp{{Mode: mode, IDs: memberIDs}})
}

// MemberOp is one membership change (used by PatchMembers).
type MemberOp struct {
	Mode MemberMode
	IDs  []string
}

// PatchMembers applies a sequence of membership ops as a single read-modify-write
// (one GetGroup, one PutDoc) under a lock, so a SCIM PATCH with several ops is all
// -or-nothing rather than partially applied (an IdP retry would otherwise
// double-apply), and concurrent membership writes cannot lose updates.
func (s *Store) PatchMembers(ctx context.Context, org, workspace, id string, ops []MemberOp) (Group, error) {
	s.memberMu.Lock()
	defer s.memberMu.Unlock()
	g, ok, err := s.GetGroup(ctx, org, workspace, id)
	if err != nil {
		return Group{}, err
	}
	if !ok {
		return Group{}, fmt.Errorf("scim: group %q not found", id)
	}
	set := map[string]bool{}
	for _, m := range g.Members {
		set[m.Value] = true
	}
	for _, op := range ops {
		switch op.Mode {
		case MembersReplace:
			set = map[string]bool{}
			for _, mid := range op.IDs {
				set[mid] = true
			}
		case MembersRemove:
			for _, mid := range op.IDs {
				delete(set, mid)
			}
		default: // MembersAdd
			for _, mid := range op.IDs {
				set[mid] = true
			}
		}
	}
	ids := make([]string, 0, len(set))
	for mid := range set {
		ids = append(ids, mid)
	}
	sort.Strings(ids) // deterministic member order (map iteration is not)
	g.Members = make([]Member, 0, len(ids))
	for _, mid := range ids {
		g.Members = append(g.Members, Member{Value: mid})
	}
	if g.Meta != nil {
		g.Meta.LastModified = s.now().UTC()
	}
	if err := store.PutDoc(ctx, s.store, groupCollection, key(org, workspace, id), g); err != nil {
		return Group{}, err
	}
	return g, nil
}

// MemberMode selects how a membership op applies the given ids.
type MemberMode int

const (
	MembersReplace MemberMode = iota // set the membership to exactly these ids
	MembersAdd                       // add these ids to the membership
	MembersRemove                    // remove these ids from the membership
)

// DeleteGroup removes a group.
func (s *Store) DeleteGroup(ctx context.Context, org, workspace, id string) error {
	return s.store.Delete(ctx, groupCollection, key(org, workspace, id))
}

// GroupsForUser returns the display names of the groups the user (by email)
// belongs to — the input to mapping SCIM group membership onto a role.
func (s *Store) GroupsForUser(ctx context.Context, org, workspace, email string) ([]string, error) {
	u, ok, err := s.byUserName(ctx, org, workspace, email)
	if err != nil || !ok {
		return nil, err
	}
	groups, err := s.ListGroups(ctx, org, workspace)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, g := range groups {
		for _, m := range g.Members {
			if m.Value == u.ID {
				names = append(names, g.DisplayName)
				break
			}
		}
	}
	return names, nil
}
