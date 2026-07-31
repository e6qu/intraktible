// SPDX-License-Identifier: AGPL-3.0-or-later

package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/e6qu/intraktible/tenancy/domain"
	"github.com/e6qu/intraktible/tenancy/projection"
)

// TenancyOrganization / TenancyWorkspace / TenancyMembership are the platform's
// public tenancy read models.
type (
	TenancyOrganization = projection.OrganizationView
	TenancyWorkspace    = projection.WorkspaceView
	TenancyMembership   = projection.MembershipView
)

// TenancyOrgCreateRequest is the platform-principal organization creation body.
type TenancyOrgCreateRequest struct {
	Key        string                    `json:"key"`
	Display    string                    `json:"display"`
	Config     domain.OrganizationConfig `json:"config"`
	AdminActor string                    `json:"admin_actor"`
	AdminName  string                    `json:"admin_name,omitempty"`
}

// TenancyOrgCreated is the organization creation result with the one-time
// org-admin credential.
type TenancyOrgCreated struct {
	EventID        string `json:"event_id"`
	Seq            uint64 `json:"seq"`
	OrgKey         string `json:"org_key"`
	AdminKeyID     string `json:"admin_key_id"`
	AdminKeySecret string `json:"admin_key_secret"`
}

// CreateOrganization creates an organization and returns its first admin key.
func (c *Client) CreateOrganization(
	ctx context.Context,
	request TenancyOrgCreateRequest,
) (TenancyOrgCreated, error) {
	return do[TenancyOrgCreated](ctx, c, http.MethodPost, "/v1/platform/orgs", request)
}

// ListOrganizations lists every organization (platform principal only).
func (c *Client) ListOrganizations(ctx context.Context) ([]TenancyOrganization, error) {
	out, err := do[struct {
		Orgs []TenancyOrganization `json:"orgs"`
	}](ctx, c, http.MethodGet, "/v1/platform/orgs", nil)
	return out.Orgs, err
}

// GetOrganization reads one organization.
func (c *Client) GetOrganization(ctx context.Context, org string) (TenancyOrganization, error) {
	return do[TenancyOrganization](ctx, c, http.MethodGet,
		"/v1/platform/orgs/"+url.PathEscape(org), nil)
}

// ConfigureOrganization updates an organization's configuration.
func (c *Client) ConfigureOrganization(
	ctx context.Context,
	org string,
	config domain.OrganizationConfig,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost,
		"/v1/platform/orgs/"+url.PathEscape(org)+"/configure",
		map[string]any{"config": config})
}

// SuspendOrganization suspends an organization.
func (c *Client) SuspendOrganization(ctx context.Context, org, reason string) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost,
		"/v1/platform/orgs/"+url.PathEscape(org)+"/suspend",
		map[string]string{"reason": reason})
}

// ResumeOrganization resumes a suspended organization.
func (c *Client) ResumeOrganization(ctx context.Context, org string) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost,
		"/v1/platform/orgs/"+url.PathEscape(org)+"/resume", nil)
}

// DeleteOrganization deletes an organization.
func (c *Client) DeleteOrganization(ctx context.Context, org, reason string) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost,
		"/v1/platform/orgs/"+url.PathEscape(org)+"/delete",
		map[string]string{"reason": reason})
}

// CreateWorkspace creates a workspace within the caller's organization.
func (c *Client) CreateWorkspace(
	ctx context.Context,
	org, key, display string,
	config domain.WorkspaceConfig,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost,
		"/v1/orgs/"+url.PathEscape(org)+"/workspaces",
		map[string]any{"key": key, "display": display, "config": config})
}

// ListWorkspaces lists the caller's organization workspaces.
func (c *Client) ListWorkspaces(ctx context.Context, org string) ([]TenancyWorkspace, error) {
	out, err := do[struct {
		Workspaces []TenancyWorkspace `json:"workspaces"`
	}](ctx, c, http.MethodGet, "/v1/orgs/"+url.PathEscape(org)+"/workspaces", nil)
	return out.Workspaces, err
}

// GetWorkspace reads one workspace.
func (c *Client) GetWorkspace(ctx context.Context, org, workspace string) (TenancyWorkspace, error) {
	return do[TenancyWorkspace](ctx, c, http.MethodGet,
		"/v1/orgs/"+url.PathEscape(org)+"/workspaces/"+url.PathEscape(workspace), nil)
}

// ConfigureWorkspace updates a workspace's configuration.
func (c *Client) ConfigureWorkspace(
	ctx context.Context,
	org, workspace string,
	config domain.WorkspaceConfig,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost,
		"/v1/orgs/"+url.PathEscape(org)+"/workspaces/"+url.PathEscape(workspace)+"/configure",
		map[string]any{"config": config})
}

// SuspendWorkspace suspends a workspace.
func (c *Client) SuspendWorkspace(
	ctx context.Context,
	org, workspace, reason string,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost,
		"/v1/orgs/"+url.PathEscape(org)+"/workspaces/"+url.PathEscape(workspace)+"/suspend",
		map[string]string{"reason": reason})
}

// ResumeWorkspace resumes a suspended workspace.
func (c *Client) ResumeWorkspace(ctx context.Context, org, workspace string) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost,
		"/v1/orgs/"+url.PathEscape(org)+"/workspaces/"+url.PathEscape(workspace)+"/resume", nil)
}

// DeleteWorkspace deletes a workspace.
func (c *Client) DeleteWorkspace(
	ctx context.Context,
	org, workspace, reason string,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost,
		"/v1/orgs/"+url.PathEscape(org)+"/workspaces/"+url.PathEscape(workspace)+"/delete",
		map[string]string{"reason": reason})
}

// ListMemberships lists a workspace's memberships.
func (c *Client) ListMemberships(ctx context.Context, org, workspace string) ([]TenancyMembership, error) {
	out, err := do[struct {
		Memberships []TenancyMembership `json:"memberships"`
	}](ctx, c, http.MethodGet,
		"/v1/orgs/"+url.PathEscape(org)+"/workspaces/"+url.PathEscape(workspace)+"/memberships", nil)
	return out.Memberships, err
}

// GrantMembership grants an actor a role in a workspace.
func (c *Client) GrantMembership(
	ctx context.Context,
	org, workspace, actor string,
	role domain.MembershipRole,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost,
		"/v1/orgs/"+url.PathEscape(org)+"/workspaces/"+url.PathEscape(workspace)+"/memberships",
		map[string]any{"actor": actor, "role": role})
}

// RevokeMembership removes an actor's membership from a workspace.
func (c *Client) RevokeMembership(
	ctx context.Context,
	org, workspace, actor, reason string,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost,
		"/v1/orgs/"+url.PathEscape(org)+"/workspaces/"+url.PathEscape(workspace)+
			"/memberships/"+url.PathEscape(actor)+"/revoke",
		map[string]string{"reason": reason})
}
