// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"errors"
	"fmt"
	"time"
)

// OrganizationStatus is the lifecycle state of an organization.
type OrganizationStatus string

const (
	OrganizationActive    OrganizationStatus = "active"
	OrganizationSuspended OrganizationStatus = "suspended"
	OrganizationDeleted   OrganizationStatus = "deleted"
)

// OrganizationConfig holds the configurable policies for an organization.
type OrganizationConfig struct {
	ResidencyRegion string `json:"residency_region"`
	Plan            string `json:"plan"`
	MaxWorkspaces   int    `json:"max_workspaces"`
	MaxDecisions    int64  `json:"max_decisions_per_month"`
	MaxArtifacts    int64  `json:"max_artifacts"`
}

// Organization is the top-level tenant entity.
type Organization struct {
	Key      string             `json:"key"`
	Display  string             `json:"display"`
	Status   OrganizationStatus `json:"status"`
	Config   OrganizationConfig `json:"config"`
	Created  time.Time          `json:"created_at"`
	Modified time.Time          `json:"modified_at"`
	Deleted  *time.Time         `json:"deleted_at,omitempty"`
}

// WorkspaceStatus is the lifecycle state of a workspace within an organization.
type WorkspaceStatus string

const (
	WorkspaceActive    WorkspaceStatus = "active"
	WorkspaceSuspended WorkspaceStatus = "suspended"
	WorkspaceDeleted   WorkspaceStatus = "deleted"
)

// WorkspaceConfig holds the configurable policies for a workspace.
type WorkspaceConfig struct {
	MaxSpendUSD   float64  `json:"max_spend_usd_per_month"`
	MaxDecisions  int64    `json:"max_decisions_per_month"`
	MaxArtifacts  int64    `json:"max_artifacts"`
	RetentionDays int      `json:"retention_days"`
	FeatureFlags  []string `json:"feature_flags"`
}

// Workspace is a sub-tenant within an organization.
type Workspace struct {
	OrgKey   string          `json:"org_key"`
	Key      string          `json:"key"`
	Display  string          `json:"display"`
	Status   WorkspaceStatus `json:"status"`
	Config   WorkspaceConfig `json:"config"`
	Created  time.Time       `json:"created_at"`
	Modified time.Time       `json:"modified_at"`
	Deleted  *time.Time      `json:"deleted_at,omitempty"`
}

// MembershipRole is the role of a member within a workspace.
type MembershipRole string

const (
	MembershipViewer   MembershipRole = "viewer"
	MembershipOperator MembershipRole = "operator"
	MembershipEditor   MembershipRole = "editor"
	MembershipApprover MembershipRole = "approver"
	MembershipAdmin    MembershipRole = "admin"
)

// MembershipStatus is the status of a membership grant.
type MembershipStatus string

const (
	MembershipActive    MembershipStatus = "active"
	MembershipSuspended MembershipStatus = "suspended"
	MembershipRemoved   MembershipStatus = "removed"
)

// Membership maps an actor to a workspace within an organization with a role.
type Membership struct {
	OrgKey    string           `json:"org_key"`
	Workspace string           `json:"workspace"`
	Actor     string           `json:"actor"`
	Role      MembershipRole   `json:"role"`
	Status    MembershipStatus `json:"status"`
	GrantedBy string           `json:"granted_by"`
	GrantedAt time.Time        `json:"granted_at"`
	RevokedAt *time.Time       `json:"revoked_at,omitempty"`
}

// NewOrganization creates a new organization with defaults.
func NewOrganization(key, display string, cfg OrganizationConfig, by string) (*Organization, error) {
	if key == "" {
		return nil, errors.New("tenancy: organization key is required")
	}
	if !isValidKey(key) {
		return nil, errors.New("tenancy: organization key must be lowercase alphanumeric with dashes, 3-64 chars")
	}
	if display == "" {
		return nil, errors.New("tenancy: organization display name is required")
	}
	now := time.Now().UTC()
	return &Organization{
		Key:      key,
		Display:  display,
		Status:   OrganizationActive,
		Config:   cfg,
		Created:  now,
		Modified: now,
	}, nil
}

// NewWorkspace creates a new workspace within an organization.
func NewWorkspace(orgKey, key, display string, cfg WorkspaceConfig) (*Workspace, error) {
	if orgKey == "" {
		return nil, errors.New("tenancy: organization key is required for workspace")
	}
	if key == "" {
		return nil, errors.New("tenancy: workspace key is required")
	}
	if !isValidKey(key) {
		return nil, errors.New("tenancy: workspace key must be lowercase alphanumeric with dashes, 3-64 chars")
	}
	if display == "" {
		return nil, errors.New("tenancy: workspace display name is required")
	}
	now := time.Now().UTC()
	return &Workspace{
		OrgKey:   orgKey,
		Key:      key,
		Display:  display,
		Status:   WorkspaceActive,
		Config:   cfg,
		Created:  now,
		Modified: now,
	}, nil
}

// NewMembership grants a membership to an actor in a workspace.
func NewMembership(orgKey, workspace, actor string, role MembershipRole, by string) (*Membership, error) {
	if orgKey == "" || workspace == "" || actor == "" {
		return nil, errors.New("tenancy: membership requires org, workspace, and actor")
	}
	if role == "" {
		return nil, errors.New("tenancy: membership role is required")
	}
	if !role.Valid() {
		return nil, fmt.Errorf("tenancy: invalid membership role %q", role)
	}
	return &Membership{
		OrgKey:    orgKey,
		Workspace: workspace,
		Actor:     actor,
		Role:      role,
		Status:    MembershipActive,
		GrantedBy: by,
		GrantedAt: time.Now().UTC(),
	}, nil
}

// Validate checks the organization key format.
func isValidKey(key string) bool {
	if len(key) < 3 || len(key) > 64 {
		return false
	}
	for _, r := range key {
		if r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// Valid reports whether the membership role is a known value.
func (r MembershipRole) Valid() bool {
	switch r {
	case MembershipViewer, MembershipOperator, MembershipEditor, MembershipApprover, MembershipAdmin:
		return true
	}
	return false
}

// Rank returns the role's ordinal level for comparison.
func (r MembershipRole) Rank() int {
	switch r {
	case MembershipViewer:
		return 1
	case MembershipOperator:
		return 2
	case MembershipEditor:
		return 3
	case MembershipApprover:
		return 4
	case MembershipAdmin:
		return 5
	default:
		return 0
	}
}

// AtLeast reports whether r meets or exceeds want.
func (r MembershipRole) AtLeast(want MembershipRole) bool {
	return r.Rank() > 0 && r.Rank() >= want.Rank()
}
