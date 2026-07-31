// SPDX-License-Identifier: AGPL-3.0-or-later

// Package command implements tenancy lifecycle validation and event emission.
package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/tenancy/domain"
	"github.com/e6qu/intraktible/tenancy/events"
)

// Handler validates and emits tenancy lifecycle events.
type Handler struct {
	log eventlog.Log
	now func() time.Time
	mu  sync.Mutex
}

// NewHandler constructs a Handler with the system clock.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the command clock for deterministic tests and seed builds.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// readTenancyStream reads all tenancy lifecycle events. Tenancy metadata is
// cross-tenant (it describes organizations themselves), so the fold reads the
// full log and filters by stream, not by a single org/workspace.
func (h *Handler) readTenancyStream(ctx context.Context) ([]eventlog.Envelope, error) {
	all, err := h.log.Read(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]eventlog.Envelope, 0, len(all))
	for _, e := range all {
		if e.Stream == events.StreamTenancy {
			out = append(out, e)
		}
	}
	return out, nil
}

// orgState folds one organization's lifecycle.
type orgState struct {
	org      domain.Organization
	exists   bool
	deleted  bool
	config   domain.OrganizationConfig
	created  time.Time
	modified time.Time
}

// workspaceState folds one workspace's lifecycle.
type workspaceState struct {
	exists   bool
	deleted  bool
	display  string
	config   domain.WorkspaceConfig
	status   domain.WorkspaceStatus
	created  time.Time
	modified time.Time
}

// membershipState folds one membership grant.
type membershipState struct {
	exists  bool
	role    domain.MembershipRole
	status  domain.MembershipStatus
	removed bool
}

// foldOrgs folds the tenancy stream into all organization states keyed by org key.
func (h *Handler) foldOrgs(ctx context.Context) (map[string]*orgState, error) {
	envelopes, err := h.readTenancyStream(ctx)
	if err != nil {
		return nil, err
	}
	orgs := make(map[string]*orgState)
	for _, e := range envelopes {
		switch e.Type {
		case events.TypeOrganizationCreated:
			var p events.OrganizationCreated
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			orgs[p.OrgKey] = &orgState{
				exists: true, config: p.Config, created: e.Time, modified: e.Time,
				org: domain.Organization{
					Key: p.OrgKey, Display: p.Display, Status: domain.OrganizationActive,
					Config: p.Config, Created: e.Time, Modified: e.Time,
				},
			}
		case events.TypeOrganizationConfigured:
			var p events.OrganizationConfigured
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if st, ok := orgs[p.OrgKey]; ok && !st.deleted {
				st.config = p.Config
				st.org.Config = p.Config
				st.org.Modified = e.Time
				st.modified = e.Time
			}
		case events.TypeOrganizationSuspended:
			var p events.OrganizationSuspended
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if st, ok := orgs[p.OrgKey]; ok && !st.deleted {
				st.org.Status = domain.OrganizationSuspended
				st.org.Modified = e.Time
			}
		case events.TypeOrganizationResumed:
			var p events.OrganizationResumed
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if st, ok := orgs[p.OrgKey]; ok && !st.deleted {
				st.org.Status = domain.OrganizationActive
				st.org.Modified = e.Time
			}
		case events.TypeOrganizationDeleted:
			var p events.OrganizationDeleted
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if st, ok := orgs[p.OrgKey]; ok {
				st.deleted = true
				st.org.Status = domain.OrganizationDeleted
				deletedAt := e.Time
				st.org.Deleted = &deletedAt
			}
		}
	}
	return orgs, nil
}

// foldWorkspaces folds the tenancy stream into all workspace states of one org,
// keyed by workspace key.
func (h *Handler) foldWorkspaces(ctx context.Context, orgKey string) (map[string]*workspaceState, error) {
	envelopes, err := h.readTenancyStream(ctx)
	if err != nil {
		return nil, err
	}
	spaces := make(map[string]*workspaceState)
	for _, e := range envelopes {
		switch e.Type {
		case events.TypeWorkspaceCreated:
			var p events.WorkspaceCreated
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.OrgKey != orgKey {
				continue
			}
			spaces[p.Key] = &workspaceState{
				exists: true, display: p.Display, config: p.Config,
				status: domain.WorkspaceActive, created: e.Time, modified: e.Time,
			}
		case events.TypeWorkspaceConfigured:
			var p events.WorkspaceConfigured
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.OrgKey != orgKey {
				continue
			}
			if st, ok := spaces[p.Key]; ok && !st.deleted {
				st.config = p.Config
				st.modified = e.Time
			}
		case events.TypeWorkspaceSuspended:
			var p events.WorkspaceSuspended
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.OrgKey != orgKey {
				continue
			}
			if st, ok := spaces[p.Key]; ok && !st.deleted {
				st.status = domain.WorkspaceSuspended
				st.modified = e.Time
			}
		case events.TypeWorkspaceResumed:
			var p events.WorkspaceResumed
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.OrgKey != orgKey {
				continue
			}
			if st, ok := spaces[p.Key]; ok && !st.deleted {
				st.status = domain.WorkspaceActive
				st.modified = e.Time
			}
		case events.TypeWorkspaceDeleted:
			var p events.WorkspaceDeleted
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.OrgKey != orgKey {
				continue
			}
			if st, ok := spaces[p.Key]; ok {
				st.deleted = true
				st.status = domain.WorkspaceDeleted
			}
		}
	}
	return spaces, nil
}

// foldMemberships folds one workspace's memberships keyed by actor.
func (h *Handler) foldMemberships(
	ctx context.Context,
	orgKey, workspace string,
) (map[string]*membershipState, error) {
	envelopes, err := h.readTenancyStream(ctx)
	if err != nil {
		return nil, err
	}
	members := make(map[string]*membershipState)
	for _, e := range envelopes {
		switch e.Type {
		case events.TypeMembershipGranted:
			var p events.MembershipGranted
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.OrgKey != orgKey || p.Workspace != workspace {
				continue
			}
			members[p.Actor] = &membershipState{
				exists: true, role: p.Role, status: domain.MembershipActive,
			}
		case events.TypeMembershipRevoked:
			var p events.MembershipRevoked
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.OrgKey != orgKey || p.Workspace != workspace {
				continue
			}
			if st, ok := members[p.Actor]; ok {
				st.status = domain.MembershipRemoved
				st.removed = true
			}
		case events.TypeMembershipSuspended:
			var p events.MembershipSuspended
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.OrgKey != orgKey || p.Workspace != workspace {
				continue
			}
			if st, ok := members[p.Actor]; ok && !st.removed {
				st.status = domain.MembershipSuspended
			}
		case events.TypeMembershipResumed:
			var p events.MembershipResumed
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.OrgKey != orgKey || p.Workspace != workspace {
				continue
			}
			if st, ok := members[p.Actor]; ok && !st.removed {
				st.status = domain.MembershipActive
			}
		}
	}
	return members, nil
}

// CreateOrganization creates a new organization. Only a platform principal may
// create organizations; the org is created active with a default "main" workspace
// and the named actor granted admin membership in it.
func (h *Handler) CreateOrganization(
	ctx context.Context,
	by identity.Identity,
	key, display string,
	config domain.OrganizationConfig,
	adminActor string,
) (eventlog.Envelope, error) {
	if _, err := domain.NewOrganization(key, display, config, by.Actor); err != nil {
		return eventlog.Envelope{}, err
	}
	if adminActor == "" {
		return eventlog.Envelope{}, errors.New("tenancy: an initial admin actor is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	orgs, err := h.foldOrgs(ctx)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if _, exists := orgs[key]; exists {
		return eventlog.Envelope{}, fmt.Errorf("tenancy: organization %q already exists", key)
	}
	if _, err := h.appendUnique(ctx, by, events.TypeOrganizationCreated, events.OrganizationCreated{
		OrgKey: key, Display: display, Config: config, CreatedBy: by.Actor,
	}, "tenancy.org.create\x00"+key); err != nil {
		return eventlog.Envelope{}, err
	}
	// Every org starts with a "main" workspace and an admin member in it.
	if _, err := h.appendUnique(ctx, by, events.TypeWorkspaceCreated, events.WorkspaceCreated{
		OrgKey: key, Key: "main", Display: "Main workspace",
		Config: domain.WorkspaceConfig{}, CreatedBy: by.Actor,
	}, "tenancy.workspace.create\x00"+key+"\x00main"); err != nil {
		return eventlog.Envelope{}, err
	}
	return h.appendUnique(ctx, by, events.TypeMembershipGranted, events.MembershipGranted{
		OrgKey: key, Workspace: "main", Actor: adminActor,
		Role: domain.MembershipAdmin, GrantedBy: by.Actor,
	}, "tenancy.membership.grant\x00"+key+"\x00main\x00"+adminActor)
}

// ConfigureOrganization updates an organization's config.
func (h *Handler) ConfigureOrganization(
	ctx context.Context,
	by identity.Identity,
	key string,
	config domain.OrganizationConfig,
) (eventlog.Envelope, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	orgs, err := h.foldOrgs(ctx)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	st, exists := orgs[key]
	if !exists || st.deleted {
		return eventlog.Envelope{}, fmt.Errorf("tenancy: organization %q is not active", key)
	}
	return h.appendUnique(ctx, by, events.TypeOrganizationConfigured, events.OrganizationConfigured{
		OrgKey: key, Config: config, ChangedBy: by.Actor,
	}, "")
}

// SuspendOrganization suspends an organization.
func (h *Handler) SuspendOrganization(
	ctx context.Context,
	by identity.Identity,
	key, reason string,
) (eventlog.Envelope, error) {
	if reason == "" {
		return eventlog.Envelope{}, errors.New("tenancy: suspension reason is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	orgs, err := h.foldOrgs(ctx)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	st, exists := orgs[key]
	if !exists || st.deleted {
		return eventlog.Envelope{}, fmt.Errorf("tenancy: organization %q is not active", key)
	}
	if st.org.Status == domain.OrganizationSuspended {
		return eventlog.Envelope{}, fmt.Errorf("tenancy: organization %q is already suspended", key)
	}
	return h.appendUnique(ctx, by, events.TypeOrganizationSuspended, events.OrganizationSuspended{
		OrgKey: key, Reason: reason, SuspendedBy: by.Actor,
	}, "")
}

// ResumeOrganization resumes a suspended organization.
func (h *Handler) ResumeOrganization(
	ctx context.Context,
	by identity.Identity,
	key string,
) (eventlog.Envelope, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	orgs, err := h.foldOrgs(ctx)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	st, exists := orgs[key]
	if !exists || st.deleted {
		return eventlog.Envelope{}, fmt.Errorf("tenancy: organization %q is not active", key)
	}
	if st.org.Status != domain.OrganizationSuspended {
		return eventlog.Envelope{}, fmt.Errorf("tenancy: organization %q is not suspended", key)
	}
	return h.appendUnique(ctx, by, events.TypeOrganizationResumed, events.OrganizationResumed{
		OrgKey: key, ResumedBy: by.Actor,
	}, "")
}

// DeleteOrganization deletes an organization. Deletion is blocked while the org
// still has active workspaces other than the default "main".
func (h *Handler) DeleteOrganization(
	ctx context.Context,
	by identity.Identity,
	key, reason string,
) (eventlog.Envelope, error) {
	if reason == "" {
		return eventlog.Envelope{}, errors.New("tenancy: deletion reason is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	orgs, err := h.foldOrgs(ctx)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	st, exists := orgs[key]
	if !exists || st.deleted {
		return eventlog.Envelope{}, fmt.Errorf("tenancy: organization %q is not active", key)
	}
	spaces, err := h.foldWorkspaces(ctx, key)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	for _, ws := range spaces {
		if !ws.deleted {
			return eventlog.Envelope{}, fmt.Errorf(
				"tenancy: organization %q still has an active workspace; delete or migrate them first", key,
			)
		}
	}
	return h.appendUnique(ctx, by, events.TypeOrganizationDeleted, events.OrganizationDeleted{
		OrgKey: key, Reason: reason, DeletedBy: by.Actor,
	}, "tenancy.org.delete\x00"+key)
}

// CreateWorkspace creates a workspace within an organization.
func (h *Handler) CreateWorkspace(
	ctx context.Context,
	by identity.Identity,
	orgKey, key, display string,
	config domain.WorkspaceConfig,
) (eventlog.Envelope, error) {
	if _, err := domain.NewWorkspace(orgKey, key, display, config); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	orgs, err := h.foldOrgs(ctx)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	st, exists := orgs[orgKey]
	if !exists || st.deleted {
		return eventlog.Envelope{}, fmt.Errorf("tenancy: organization %q is not active", orgKey)
	}
	if st.org.Status == domain.OrganizationSuspended {
		return eventlog.Envelope{}, fmt.Errorf("tenancy: organization %q is suspended", orgKey)
	}
	spaces, err := h.foldWorkspaces(ctx, orgKey)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if _, exists := spaces[key]; exists {
		return eventlog.Envelope{}, fmt.Errorf("tenancy: workspace %q already exists in %q", key, orgKey)
	}
	if st.config.MaxWorkspaces > 0 && len(spaces) >= st.config.MaxWorkspaces {
		return eventlog.Envelope{}, fmt.Errorf(
			"tenancy: organization %q workspace quota (%d) is exhausted", orgKey, st.config.MaxWorkspaces,
		)
	}
	return h.appendUnique(ctx, by, events.TypeWorkspaceCreated, events.WorkspaceCreated{
		OrgKey: orgKey, Key: key, Display: display, Config: config, CreatedBy: by.Actor,
	}, "tenancy.workspace.create\x00"+orgKey+"\x00"+key)
}

// ConfigureWorkspace updates a workspace's config.
func (h *Handler) ConfigureWorkspace(
	ctx context.Context,
	by identity.Identity,
	orgKey, key string,
	config domain.WorkspaceConfig,
) (eventlog.Envelope, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	spaces, err := h.activeWorkspace(ctx, orgKey, key)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	_ = spaces
	return h.appendUnique(ctx, by, events.TypeWorkspaceConfigured, events.WorkspaceConfigured{
		OrgKey: orgKey, Key: key, Config: config, ChangedBy: by.Actor,
	}, "")
}

// SuspendWorkspace suspends a workspace.
func (h *Handler) SuspendWorkspace(
	ctx context.Context,
	by identity.Identity,
	orgKey, key, reason string,
) (eventlog.Envelope, error) {
	if reason == "" {
		return eventlog.Envelope{}, errors.New("tenancy: suspension reason is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	spaces, err := h.activeWorkspace(ctx, orgKey, key)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if spaces.status == domain.WorkspaceSuspended {
		return eventlog.Envelope{}, fmt.Errorf("tenancy: workspace %q is already suspended", key)
	}
	return h.appendUnique(ctx, by, events.TypeWorkspaceSuspended, events.WorkspaceSuspended{
		OrgKey: orgKey, Key: key, Reason: reason, SuspendedBy: by.Actor,
	}, "")
}

// ResumeWorkspace resumes a suspended workspace.
func (h *Handler) ResumeWorkspace(
	ctx context.Context,
	by identity.Identity,
	orgKey, key string,
) (eventlog.Envelope, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	spaces, err := h.activeWorkspace(ctx, orgKey, key)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if spaces.status != domain.WorkspaceSuspended {
		return eventlog.Envelope{}, fmt.Errorf("tenancy: workspace %q is not suspended", key)
	}
	return h.appendUnique(ctx, by, events.TypeWorkspaceResumed, events.WorkspaceResumed{
		OrgKey: orgKey, Key: key, ResumedBy: by.Actor,
	}, "")
}

// DeleteWorkspace deletes a workspace.
func (h *Handler) DeleteWorkspace(
	ctx context.Context,
	by identity.Identity,
	orgKey, key, reason string,
) (eventlog.Envelope, error) {
	if reason == "" {
		return eventlog.Envelope{}, errors.New("tenancy: deletion reason is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	spaces, err := h.activeWorkspace(ctx, orgKey, key)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	_ = spaces
	return h.appendUnique(ctx, by, events.TypeWorkspaceDeleted, events.WorkspaceDeleted{
		OrgKey: orgKey, Key: key, Reason: reason, DeletedBy: by.Actor,
	}, "tenancy.workspace.delete\x00"+orgKey+"\x00"+key)
}

// activeWorkspace folds and returns the active (non-deleted) workspace state.
func (h *Handler) activeWorkspace(
	ctx context.Context,
	orgKey, key string,
) (*workspaceState, error) {
	spaces, err := h.foldWorkspaces(ctx, orgKey)
	if err != nil {
		return nil, err
	}
	st, exists := spaces[key]
	if !exists || st.deleted {
		return nil, fmt.Errorf("tenancy: workspace %q is not active in %q", key, orgKey)
	}
	return st, nil
}

// GrantMembership grants an actor a role in a workspace.
func (h *Handler) GrantMembership(
	ctx context.Context,
	by identity.Identity,
	orgKey, workspace, actor string,
	role domain.MembershipRole,
) (eventlog.Envelope, error) {
	if _, err := domain.NewMembership(orgKey, workspace, actor, role, by.Actor); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.activeWorkspace(ctx, orgKey, workspace); err != nil {
		return eventlog.Envelope{}, err
	}
	members, err := h.foldMemberships(ctx, orgKey, workspace)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if st, ok := members[actor]; ok && !st.removed {
		return eventlog.Envelope{}, fmt.Errorf(
			"tenancy: %q already has an active membership in %q/%q", actor, orgKey, workspace,
		)
	}
	return h.appendUnique(ctx, by, events.TypeMembershipGranted, events.MembershipGranted{
		OrgKey: orgKey, Workspace: workspace, Actor: actor, Role: role, GrantedBy: by.Actor,
	}, "tenancy.membership.grant\x00"+orgKey+"\x00"+workspace+"\x00"+actor)
}

// RevokeMembership removes an actor's membership from a workspace.
func (h *Handler) RevokeMembership(
	ctx context.Context,
	by identity.Identity,
	orgKey, workspace, actor, reason string,
) (eventlog.Envelope, error) {
	if reason == "" {
		return eventlog.Envelope{}, errors.New("tenancy: revocation reason is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	members, err := h.foldMemberships(ctx, orgKey, workspace)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	st, ok := members[actor]
	if !ok || st.removed {
		return eventlog.Envelope{}, fmt.Errorf(
			"tenancy: %q has no active membership in %q/%q", actor, orgKey, workspace,
		)
	}
	// Last-admin safety: refuse to remove the final active admin of a workspace.
	if st.role == domain.MembershipAdmin {
		admins := 0
		for _, m := range members {
			if m.role == domain.MembershipAdmin && !m.removed && m.status == domain.MembershipActive {
				admins++
			}
		}
		if admins <= 1 {
			return eventlog.Envelope{}, fmt.Errorf(
				"tenancy: %q is the last active admin of %q/%q; grant another admin first",
				actor, orgKey, workspace,
			)
		}
	}
	return h.appendUnique(ctx, by, events.TypeMembershipRevoked, events.MembershipRevoked{
		OrgKey: orgKey, Workspace: workspace, Actor: actor, RevokedBy: by.Actor, Reason: reason,
	}, "tenancy.membership.revoke\x00"+orgKey+"\x00"+workspace+"\x00"+actor)
}

func (h *Handler) appendUnique(
	ctx context.Context,
	by identity.Identity,
	typ string,
	payload any,
	claim string,
) (eventlog.Envelope, error) {
	return eventlog.AppendClaim(ctx, h.log, by, events.StreamTenancy, typ, h.now(), payload, claim)
}

func decode(envelope eventlog.Envelope, payload any) error {
	if err := json.Unmarshal(envelope.Payload, payload); err != nil {
		return fmt.Errorf("tenancy: decode %s seq %d: %w", envelope.Type, envelope.Seq, err)
	}
	return nil
}
