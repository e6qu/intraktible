// SPDX-License-Identifier: AGPL-3.0-or-later

// Package events defines immutable tenancy lifecycle event payloads.
package events

import (
	"github.com/e6qu/intraktible/tenancy/domain"
)

// StreamTenancy is the tenant organization/workspace/membership lifecycle stream.
const StreamTenancy = "tenancy"

const (
	TypeOrganizationCreated    = "tenancy.organization.created"
	TypeOrganizationConfigured = "tenancy.organization.configured"
	TypeOrganizationSuspended  = "tenancy.organization.suspended"
	TypeOrganizationResumed    = "tenancy.organization.resumed"
	TypeOrganizationDeleted    = "tenancy.organization.deleted"
)

const (
	TypeWorkspaceCreated    = "tenancy.workspace.created"
	TypeWorkspaceConfigured = "tenancy.workspace.configured"
	TypeWorkspaceSuspended  = "tenancy.workspace.suspended"
	TypeWorkspaceResumed    = "tenancy.workspace.resumed"
	TypeWorkspaceDeleted    = "tenancy.workspace.deleted"
)

const (
	TypeMembershipGranted   = "tenancy.membership.granted"
	TypeMembershipRevoked   = "tenancy.membership.revoked"
	TypeMembershipSuspended = "tenancy.membership.suspended"
	TypeMembershipResumed   = "tenancy.membership.resumed"
)

// OrganizationCreated records the creation of a new organization.
type OrganizationCreated struct {
	OrgKey    string                    `json:"org_key"`
	Display   string                    `json:"display"`
	Config    domain.OrganizationConfig `json:"config"`
	CreatedBy string                    `json:"created_by"`
}

// OrganizationConfigured records a configuration change to an organization.
type OrganizationConfigured struct {
	OrgKey    string                    `json:"org_key"`
	Config    domain.OrganizationConfig `json:"config"`
	ChangedBy string                    `json:"changed_by"`
}

// OrganizationSuspended records an organization suspension.
type OrganizationSuspended struct {
	OrgKey      string `json:"org_key"`
	Reason      string `json:"reason"`
	SuspendedBy string `json:"suspended_by"`
}

// OrganizationResumed records an organization resumption.
type OrganizationResumed struct {
	OrgKey    string `json:"org_key"`
	ResumedBy string `json:"resumed_by"`
}

// OrganizationDeleted records an organization deletion.
type OrganizationDeleted struct {
	OrgKey    string `json:"org_key"`
	Reason    string `json:"reason"`
	DeletedBy string `json:"deleted_by"`
}

// WorkspaceCreated records the creation of a workspace.
type WorkspaceCreated struct {
	OrgKey    string                 `json:"org_key"`
	Key       string                 `json:"key"`
	Display   string                 `json:"display"`
	Config    domain.WorkspaceConfig `json:"config"`
	CreatedBy string                 `json:"created_by"`
}

// WorkspaceConfigured records a workspace configuration change.
type WorkspaceConfigured struct {
	OrgKey    string                 `json:"org_key"`
	Key       string                 `json:"key"`
	Config    domain.WorkspaceConfig `json:"config"`
	ChangedBy string                 `json:"changed_by"`
}

// WorkspaceSuspended records a workspace suspension.
type WorkspaceSuspended struct {
	OrgKey      string `json:"org_key"`
	Key         string `json:"key"`
	Reason      string `json:"reason"`
	SuspendedBy string `json:"suspended_by"`
}

// WorkspaceResumed records a workspace resumption.
type WorkspaceResumed struct {
	OrgKey    string `json:"org_key"`
	Key       string `json:"key"`
	ResumedBy string `json:"resumed_by"`
}

// WorkspaceDeleted records a workspace deletion.
type WorkspaceDeleted struct {
	OrgKey    string `json:"org_key"`
	Key       string `json:"key"`
	Reason    string `json:"reason"`
	DeletedBy string `json:"deleted_by"`
}

// MembershipGranted records a membership grant.
type MembershipGranted struct {
	OrgKey    string                `json:"org_key"`
	Workspace string                `json:"workspace"`
	Actor     string                `json:"actor"`
	Role      domain.MembershipRole `json:"role"`
	GrantedBy string                `json:"granted_by"`
}

// MembershipRevoked records a membership revocation.
type MembershipRevoked struct {
	OrgKey    string `json:"org_key"`
	Workspace string `json:"workspace"`
	Actor     string `json:"actor"`
	RevokedBy string `json:"revoked_by"`
	Reason    string `json:"reason"`
}

// MembershipSuspended records a membership suspension.
type MembershipSuspended struct {
	OrgKey      string `json:"org_key"`
	Workspace   string `json:"workspace"`
	Actor       string `json:"actor"`
	SuspendedBy string `json:"suspended_by"`
	Reason      string `json:"reason"`
}

// MembershipResumed records a membership resumption.
type MembershipResumed struct {
	OrgKey    string `json:"org_key"`
	Workspace string `json:"workspace"`
	Actor     string `json:"actor"`
	ResumedBy string `json:"resumed_by"`
}
