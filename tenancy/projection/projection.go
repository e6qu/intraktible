// SPDX-License-Identifier: AGPL-3.0-or-later

// Package projection contains replayable tenancy read models.
package projection

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/tenancy/domain"
	"github.com/e6qu/intraktible/tenancy/events"
)

const (
	// CollectionOrgs stores organization read models.
	CollectionOrgs = "tenancy_orgs"
	// CollectionWorkspaces stores workspace read models.
	CollectionWorkspaces = "tenancy_workspaces"
	// CollectionMemberships stores membership read models.
	CollectionMemberships = "tenancy_memberships"
)

// OrganizationView is one organization's read model.
type OrganizationView struct {
	Key       string                    `json:"key"`
	Display   string                    `json:"display"`
	Status    domain.OrganizationStatus `json:"status"`
	Config    domain.OrganizationConfig `json:"config"`
	CreatedAt string                    `json:"created_at"`
	UpdatedAt string                    `json:"updated_at"`
	DeletedAt string                    `json:"deleted_at,omitempty"`
}

// WorkspaceView is one workspace's read model.
type WorkspaceView struct {
	OrgKey    string                 `json:"org_key"`
	Key       string                 `json:"key"`
	Display   string                 `json:"display"`
	Status    domain.WorkspaceStatus `json:"status"`
	Config    domain.WorkspaceConfig `json:"config"`
	CreatedAt string                 `json:"created_at"`
	UpdatedAt string                 `json:"updated_at"`
	DeletedAt string                 `json:"deleted_at,omitempty"`
}

// MembershipView is one membership's read model.
type MembershipView struct {
	OrgKey    string                  `json:"org_key"`
	Workspace string                  `json:"workspace"`
	Actor     string                  `json:"actor"`
	Role      domain.MembershipRole   `json:"role"`
	Status    domain.MembershipStatus `json:"status"`
	GrantedBy string                  `json:"granted_by"`
	GrantedAt string                  `json:"granted_at"`
	RevokedAt string                  `json:"revoked_at,omitempty"`
}

// Projector folds tenancy lifecycle events into read models.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "tenancy" }

// Collections lists the store collections this projector owns (reset on rebuild).
func (Projector) Collections() []string {
	return []string{CollectionOrgs, CollectionWorkspaces, CollectionMemberships}
}

// Apply folds one tenancy event into the read models.
func (Projector) Apply(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	switch envelope.Type {
	case events.TypeOrganizationCreated:
		var p events.OrganizationCreated
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return store.PutDoc(ctx, st, CollectionOrgs, OrgKey(p.OrgKey), OrganizationView{
			Key: p.OrgKey, Display: p.Display, Status: domain.OrganizationActive,
			Config: p.Config, CreatedAt: ts(envelope), UpdatedAt: ts(envelope),
		})
	case events.TypeOrganizationConfigured:
		var p events.OrganizationConfigured
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutateOrg(ctx, st, p.OrgKey, envelope, func(v *OrganizationView) {
			v.Config = p.Config
		})
	case events.TypeOrganizationSuspended:
		var p events.OrganizationSuspended
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutateOrg(ctx, st, p.OrgKey, envelope, func(v *OrganizationView) {
			v.Status = domain.OrganizationSuspended
		})
	case events.TypeOrganizationResumed:
		var p events.OrganizationResumed
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutateOrg(ctx, st, p.OrgKey, envelope, func(v *OrganizationView) {
			v.Status = domain.OrganizationActive
		})
	case events.TypeOrganizationDeleted:
		var p events.OrganizationDeleted
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutateOrg(ctx, st, p.OrgKey, envelope, func(v *OrganizationView) {
			v.Status = domain.OrganizationDeleted
			v.DeletedAt = ts(envelope)
		})
	case events.TypeWorkspaceCreated:
		var p events.WorkspaceCreated
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return store.PutDoc(ctx, st, CollectionWorkspaces, WorkspaceKey(p.OrgKey, p.Key), WorkspaceView{
			OrgKey: p.OrgKey, Key: p.Key, Display: p.Display, Status: domain.WorkspaceActive,
			Config: p.Config, CreatedAt: ts(envelope), UpdatedAt: ts(envelope),
		})
	case events.TypeWorkspaceConfigured:
		var p events.WorkspaceConfigured
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutateWorkspace(ctx, st, p.OrgKey, p.Key, envelope, func(v *WorkspaceView) {
			v.Config = p.Config
		})
	case events.TypeWorkspaceSuspended:
		var p events.WorkspaceSuspended
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutateWorkspace(ctx, st, p.OrgKey, p.Key, envelope, func(v *WorkspaceView) {
			v.Status = domain.WorkspaceSuspended
		})
	case events.TypeWorkspaceResumed:
		var p events.WorkspaceResumed
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutateWorkspace(ctx, st, p.OrgKey, p.Key, envelope, func(v *WorkspaceView) {
			v.Status = domain.WorkspaceActive
		})
	case events.TypeWorkspaceDeleted:
		var p events.WorkspaceDeleted
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutateWorkspace(ctx, st, p.OrgKey, p.Key, envelope, func(v *WorkspaceView) {
			v.Status = domain.WorkspaceDeleted
			v.DeletedAt = ts(envelope)
		})
	case events.TypeMembershipGranted:
		var p events.MembershipGranted
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return store.PutDoc(ctx, st, CollectionMemberships, MembershipKey(p.OrgKey, p.Workspace, p.Actor),
			MembershipView{
				OrgKey: p.OrgKey, Workspace: p.Workspace, Actor: p.Actor, Role: p.Role,
				Status: domain.MembershipActive, GrantedBy: p.GrantedBy, GrantedAt: ts(envelope),
			})
	case events.TypeMembershipRevoked:
		var p events.MembershipRevoked
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutateMembership(ctx, st, p.OrgKey, p.Workspace, p.Actor, envelope,
			func(v *MembershipView) {
				v.Status = domain.MembershipRemoved
				v.RevokedAt = ts(envelope)
			})
	case events.TypeMembershipSuspended:
		var p events.MembershipSuspended
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutateMembership(ctx, st, p.OrgKey, p.Workspace, p.Actor, envelope,
			func(v *MembershipView) {
				v.Status = domain.MembershipSuspended
			})
	case events.TypeMembershipResumed:
		var p events.MembershipResumed
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutateMembership(ctx, st, p.OrgKey, p.Workspace, p.Actor, envelope,
			func(v *MembershipView) {
				v.Status = domain.MembershipActive
			})
	default:
		return nil
	}
}

func mutateOrg(
	ctx context.Context, st store.Store, key string, envelope eventlog.Envelope,
	mutate func(*OrganizationView),
) error {
	view, found, err := store.GetDoc[OrganizationView](ctx, st, CollectionOrgs, OrgKey(key))
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("tenancy projection: organization %q is missing at seq %d", key, envelope.Seq)
	}
	mutate(&view)
	view.UpdatedAt = ts(envelope)
	return store.PutDoc(ctx, st, CollectionOrgs, OrgKey(key), view)
}

func mutateWorkspace(
	ctx context.Context, st store.Store, orgKey2, key string, envelope eventlog.Envelope,
	mutate func(*WorkspaceView),
) error {
	k := WorkspaceKey(orgKey2, key)
	view, found, err := store.GetDoc[WorkspaceView](ctx, st, CollectionWorkspaces, k)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf(
			"tenancy projection: workspace %q/%q is missing at seq %d", orgKey2, key, envelope.Seq,
		)
	}
	mutate(&view)
	view.UpdatedAt = ts(envelope)
	return store.PutDoc(ctx, st, CollectionWorkspaces, k, view)
}

func mutateMembership(
	ctx context.Context, st store.Store, orgKey2, ws, actor string, envelope eventlog.Envelope,
	mutate func(*MembershipView),
) error {
	k := MembershipKey(orgKey2, ws, actor)
	view, found, err := store.GetDoc[MembershipView](ctx, st, CollectionMemberships, k)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf(
			"tenancy projection: membership %q in %q/%q is missing at seq %d", actor, orgKey2, ws, envelope.Seq,
		)
	}
	mutate(&view)
	return store.PutDoc(ctx, st, CollectionMemberships, k, view)
}

// OrgKey / WorkspaceKey / MembershipKey build the tenancy read-model store keys.
// Tenancy metadata is cross-tenant, so keys use an empty org/workspace envelope
// and '/' separators inside the id segment (never NUL bytes — Postgres text
// columns cannot store 0x00, and org/workspace keys are validated to exclude '/').
func OrgKey(key string) string { return store.Key("", "", "org/"+key) }
func OrgPrefix() string        { return store.Key("", "", "org/") }
func WorkspaceKey(org, key string) string {
	return store.Key("", "", "ws/"+org+"/"+key)
}
func WorkspacePrefix(org string) string { return store.Key("", "", "ws/"+org+"/") }
func MembershipKey(org, ws, actor string) string {
	return store.Key("", "", "member/"+org+"/"+ws+"/"+actor)
}
func MembershipPrefix(org, ws string) string {
	return store.Key("", "", "member/"+org+"/"+ws+"/")
}

func ts(envelope eventlog.Envelope) string {
	return envelope.Time.Format("2006-01-02T15:04:05.999999999Z07:00")
}

func decode(envelope eventlog.Envelope, payload any) error {
	if err := json.Unmarshal(envelope.Payload, payload); err != nil {
		return fmt.Errorf("tenancy projection: decode %s seq %d: %w", envelope.Type, envelope.Seq, err)
	}
	return nil
}
