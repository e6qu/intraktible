// SPDX-License-Identifier: AGPL-3.0-or-later

// Package service exposes tenancy administration over HTTP: platform-level
// organization lifecycle and org-level workspace/membership administration.
package service

import (
	"context"
	"errors"
	"net/http"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/tenancy/command"
	"github.com/e6qu/intraktible/tenancy/domain"
	"github.com/e6qu/intraktible/tenancy/projection"
)

// Service is the tenancy HTTP shell.
type Service struct {
	cmd     *command.Handler
	store   store.Store
	apiKeys *auth.StoreAPIKeys
}

// New constructs a tenancy Service.
func New(cmd *command.Handler, st store.Store, apiKeys *auth.StoreAPIKeys) *Service {
	return &Service{cmd: cmd, store: st, apiKeys: apiKeys}
}

// Routes registers the tenancy administration endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/platform/orgs", s.createOrg)
	mux.HandleFunc("GET /v1/platform/orgs", s.listOrgs)
	mux.HandleFunc("GET /v1/platform/orgs/{org}", s.getOrg)
	mux.HandleFunc("POST /v1/platform/orgs/{org}/suspend", s.suspendOrg)
	mux.HandleFunc("POST /v1/platform/orgs/{org}/resume", s.resumeOrg)
	mux.HandleFunc("POST /v1/platform/orgs/{org}/delete", s.deleteOrg)
	mux.HandleFunc("POST /v1/platform/orgs/{org}/configure", s.configureOrg)

	mux.HandleFunc("GET /v1/orgs/{org}/workspaces", s.listWorkspaces)
	mux.HandleFunc("POST /v1/orgs/{org}/workspaces", s.createWorkspace)
	mux.HandleFunc("GET /v1/orgs/{org}/workspaces/{workspace}", s.getWorkspace)
	mux.HandleFunc("POST /v1/orgs/{org}/workspaces/{workspace}/configure", s.configureWorkspace)
	mux.HandleFunc("POST /v1/orgs/{org}/workspaces/{workspace}/suspend", s.suspendWorkspace)
	mux.HandleFunc("POST /v1/orgs/{org}/workspaces/{workspace}/resume", s.resumeWorkspace)
	mux.HandleFunc("POST /v1/orgs/{org}/workspaces/{workspace}/delete", s.deleteWorkspace)

	mux.HandleFunc("GET /v1/orgs/{org}/workspaces/{workspace}/memberships", s.listMemberships)
	mux.HandleFunc("POST /v1/orgs/{org}/workspaces/{workspace}/memberships", s.grantMembership)
	mux.HandleFunc(
		"POST /v1/orgs/{org}/workspaces/{workspace}/memberships/{actor}/revoke", s.revokeMembership,
	)
}

// platformCaller resolves the caller and requires the platform-admin flag.
// Platform administration (creating/suspending/deleting organizations) is a
// cross-tenant authority distinct from any tenant's own admin role.
func (s *Service) platformCaller(
	w http.ResponseWriter,
	r *http.Request,
) (identity.Identity, bool) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return identity.Identity{}, false
	}
	p, ok := httpx.PrincipalOf(r.Context())
	if !ok || !p.Platform {
		httpx.Error(w, http.StatusForbidden, errors.New(
			"tenancy: platform administration requires a platform principal",
		))
		return identity.Identity{}, false
	}
	return id, true
}

type createOrgRequest struct {
	Key        string                    `json:"key"`
	Display    string                    `json:"display"`
	Config     domain.OrganizationConfig `json:"config"`
	AdminActor string                    `json:"admin_actor"`
	AdminName  string                    `json:"admin_name"`
}

// createOrg creates a new organization with its default "main" workspace and an
// admin membership, then mints the organization's first admin API key, returned
// once. Only a platform principal may create organizations.
func (s *Service) createOrg(w http.ResponseWriter, r *http.Request) {
	id, ok := s.platformCaller(w, r)
	if !ok {
		return
	}
	var request createOrgRequest
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if request.AdminName == "" {
		request.AdminName = request.AdminActor
	}
	envelope, err := s.cmd.CreateOrganization(
		r.Context(), id, request.Key, request.Display, request.Config, request.AdminActor,
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	// Mint the new organization's first admin credential. This is the org's only
	// way to authenticate until a managed key is issued; the secret is shown once.
	key, secret, err := s.apiKeys.Create(r.Context(), auth.ManagedAPIKey{
		Name: request.AdminName + " org-admin bootstrap",
		Identity: identity.Identity{
			Org: request.Key, Workspace: "main", Actor: request.AdminActor,
		},
		Scope: auth.ScopeAll, Role: auth.RoleAdmin,
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"event_id": envelope.ID, "seq": envelope.Seq,
		"org_key": request.Key, "admin_key_id": key.ID, "admin_key_secret": secret,
	})
}

type orgRequest struct {
	Reason string `json:"reason"`
}

type configureOrgRequest struct {
	Config domain.OrganizationConfig `json:"config"`
}

func (s *Service) suspendOrg(w http.ResponseWriter, r *http.Request) {
	id, ok := s.platformCaller(w, r)
	if !ok {
		return
	}
	var request orgRequest
	httpx.Emit(w, r, &request, func(caller identity.Identity) (eventlog.Envelope, error) {
		_ = caller
		return s.cmd.SuspendOrganization(r.Context(), id, r.PathValue("org"), request.Reason)
	})
}

func (s *Service) resumeOrg(w http.ResponseWriter, r *http.Request) {
	id, ok := s.platformCaller(w, r)
	if !ok {
		return
	}
	httpx.Act(w, r, func(caller identity.Identity) (eventlog.Envelope, error) {
		_ = caller
		return s.cmd.ResumeOrganization(r.Context(), id, r.PathValue("org"))
	})
}

func (s *Service) deleteOrg(w http.ResponseWriter, r *http.Request) {
	id, ok := s.platformCaller(w, r)
	if !ok {
		return
	}
	var request orgRequest
	httpx.Emit(w, r, &request, func(caller identity.Identity) (eventlog.Envelope, error) {
		_ = caller
		return s.cmd.DeleteOrganization(r.Context(), id, r.PathValue("org"), request.Reason)
	})
}

func (s *Service) configureOrg(w http.ResponseWriter, r *http.Request) {
	id, ok := s.platformCaller(w, r)
	if !ok {
		return
	}
	var request configureOrgRequest
	httpx.Emit(w, r, &request, func(caller identity.Identity) (eventlog.Envelope, error) {
		_ = caller
		return s.cmd.ConfigureOrganization(r.Context(), id, r.PathValue("org"), request.Config)
	})
}

func (s *Service) listOrgs(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.platformCaller(w, r); !ok {
		return
	}
	items, err := listOrgs(r.Context(), s.store)
	httpx.WriteList(w, "orgs", items, err)
}

func (s *Service) getOrg(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.platformCaller(w, r); !ok {
		return
	}
	view, found, err := readOrg(r.Context(), s.store, r.PathValue("org"))
	httpx.WriteOne(w, view, found, err, "tenancy: organization not found")
}

// orgCaller resolves the caller for workspace/membership administration. An org
// admin manages their own organization; a platform principal may administer any
// organization's workspaces (the platform operator's cross-tenant authority).
func (s *Service) orgCaller(
	w http.ResponseWriter,
	r *http.Request,
) (identity.Identity, bool) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return identity.Identity{}, false
	}
	if p, isPrincipal := httpx.PrincipalOf(r.Context()); isPrincipal && p.Platform {
		return id, true
	}
	if id.Org != r.PathValue("org") {
		httpx.Error(w, http.StatusForbidden, errors.New(
			"tenancy: workspace administration is scoped to the caller's organization",
		))
		return identity.Identity{}, false
	}
	return id, true
}

type createWorkspaceRequest struct {
	Key     string                 `json:"key"`
	Display string                 `json:"display"`
	Config  domain.WorkspaceConfig `json:"config"`
}

func (s *Service) createWorkspace(w http.ResponseWriter, r *http.Request) {
	id, ok := s.orgCaller(w, r)
	if !ok {
		return
	}
	var request createWorkspaceRequest
	httpx.Emit(w, r, &request, func(caller identity.Identity) (eventlog.Envelope, error) {
		_ = caller
		return s.cmd.CreateWorkspace(
			r.Context(), id, r.PathValue("org"), request.Key, request.Display, request.Config,
		)
	})
}

func (s *Service) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.orgCaller(w, r); !ok {
		return
	}
	items, err := listWorkspaces(r.Context(), s.store, id2org(r))
	httpx.WriteList(w, "workspaces", items, err)
}

func (s *Service) getWorkspace(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.orgCaller(w, r); !ok {
		return
	}
	view, found, err := readWorkspace(
		r.Context(), s.store, id2org(r), r.PathValue("workspace"),
	)
	httpx.WriteOne(w, view, found, err, "tenancy: workspace not found")
}

func (s *Service) configureWorkspace(w http.ResponseWriter, r *http.Request) {
	id, ok := s.orgCaller(w, r)
	if !ok {
		return
	}
	var request struct {
		Config domain.WorkspaceConfig `json:"config"`
	}
	httpx.Emit(w, r, &request, func(caller identity.Identity) (eventlog.Envelope, error) {
		_ = caller
		return s.cmd.ConfigureWorkspace(r.Context(), id, r.PathValue("org"), r.PathValue("workspace"), request.Config)
	})
}

func (s *Service) suspendWorkspace(w http.ResponseWriter, r *http.Request) {
	s.workspaceAction(w, r, func(id identity.Identity, key, reason string) (eventlog.Envelope, error) {
		return s.cmd.SuspendWorkspace(r.Context(), id, r.PathValue("org"), key, reason)
	})
}

func (s *Service) resumeWorkspace(w http.ResponseWriter, r *http.Request) {
	id, ok := s.orgCaller(w, r)
	if !ok {
		return
	}
	httpx.Act(w, r, func(caller identity.Identity) (eventlog.Envelope, error) {
		_ = caller
		return s.cmd.ResumeWorkspace(r.Context(), id, r.PathValue("org"), r.PathValue("workspace"))
	})
}

func (s *Service) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	s.workspaceAction(w, r, func(id identity.Identity, key, reason string) (eventlog.Envelope, error) {
		return s.cmd.DeleteWorkspace(r.Context(), id, r.PathValue("org"), key, reason)
	})
}

// workspaceAction is the shared shape for reason-gated workspace lifecycle
// mutations (suspend/delete): org-scope the caller, decode the reason, and run
// the command against the path's workspace.
func (s *Service) workspaceAction(
	w http.ResponseWriter,
	r *http.Request,
	run func(id identity.Identity, workspace, reason string) (eventlog.Envelope, error),
) {
	id, ok := s.orgCaller(w, r)
	if !ok {
		return
	}
	var request orgRequest
	httpx.Emit(w, r, &request, func(caller identity.Identity) (eventlog.Envelope, error) {
		_ = caller
		return run(id, r.PathValue("workspace"), request.Reason)
	})
}

func (s *Service) listMemberships(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.orgCaller(w, r); !ok {
		return
	}
	items, err := listMemberships(r.Context(), s.store, id2org(r), r.PathValue("workspace"))
	httpx.WriteList(w, "memberships", items, err)
}

type grantMembershipRequest struct {
	Actor string                `json:"actor"`
	Role  domain.MembershipRole `json:"role"`
}

func (s *Service) grantMembership(w http.ResponseWriter, r *http.Request) {
	id, ok := s.orgCaller(w, r)
	if !ok {
		return
	}
	var request grantMembershipRequest
	httpx.Emit(w, r, &request, func(caller identity.Identity) (eventlog.Envelope, error) {
		_ = caller
		return s.cmd.GrantMembership(
			r.Context(), id, r.PathValue("org"), r.PathValue("workspace"), request.Actor, request.Role,
		)
	})
}

func (s *Service) revokeMembership(w http.ResponseWriter, r *http.Request) {
	id, ok := s.orgCaller(w, r)
	if !ok {
		return
	}
	var request orgRequest
	httpx.Emit(w, r, &request, func(caller identity.Identity) (eventlog.Envelope, error) {
		_ = caller
		return s.cmd.RevokeMembership(
			r.Context(), id, r.PathValue("org"), r.PathValue("workspace"), r.PathValue("actor"), request.Reason,
		)
	})
}

func id2org(r *http.Request) string { return r.PathValue("org") }

// Read helpers over the tenancy projection collections. Tenancy read models are
// keyed by a dedicated org-less prefix (they are cross-tenant metadata), so these
// helpers query the projection's org/workspace prefix directly.

func readOrg(
	ctx context.Context,
	st store.Store,
	key string,
) (projection.OrganizationView, bool, error) {
	return store.GetDoc[projection.OrganizationView](
		ctx, st, projection.CollectionOrgs, projection.OrgKey(key),
	)
}

func listOrgs(ctx context.Context, st store.Store) ([]projection.OrganizationView, error) {
	return store.QueryDocs(ctx, st, projection.CollectionOrgs, projection.OrgPrefix(),
		nil,
		func(a, b projection.OrganizationView) bool { return a.Key < b.Key })
}

func readWorkspace(
	ctx context.Context,
	st store.Store,
	org, key string,
) (projection.WorkspaceView, bool, error) {
	return store.GetDoc[projection.WorkspaceView](
		ctx, st, projection.CollectionWorkspaces, projection.WorkspaceKey(org, key),
	)
}

func listWorkspaces(
	ctx context.Context,
	st store.Store,
	org string,
) ([]projection.WorkspaceView, error) {
	return store.QueryDocs(ctx, st, projection.CollectionWorkspaces,
		projection.WorkspacePrefix(org),
		nil,
		func(a, b projection.WorkspaceView) bool { return a.Key < b.Key })
}

func listMemberships(
	ctx context.Context,
	st store.Store,
	org, workspace string,
) ([]projection.MembershipView, error) {
	return store.QueryDocs(ctx, st, projection.CollectionMemberships,
		projection.MembershipPrefix(org, workspace),
		nil,
		func(a, b projection.MembershipView) bool { return a.Actor < b.Actor })
}
