// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"testing"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
	"github.com/e6qu/intraktible/tenancy/command"
	"github.com/e6qu/intraktible/tenancy/projection"
	"github.com/e6qu/intraktible/tenancy/service"
)

// startTenancy assembles the tenancy module over the real HTTP stack. The seeded
// key is a platform principal (Platform: true) so it can create organizations.
func startTenancy(t *testing.T) *testutil.API {
	log, st := testutil.NewLogStore(t)
	platformID := identity.Identity{Org: "default", Workspace: "main", Actor: "bootstrap"}
	apiKeys := auth.NewStoreAPIKeys(st)
	svc := service.New(command.NewHandler(log), st, apiKeys)
	api := testutil.StartAPIScoped(
		t, log, st, "platform-key", auth.ScopeAll, platformID,
		svc.Routes, projection.Projector{},
	)
	// Re-seed the harness key as a platform principal.
	api.AddKey("platform-key", auth.APIKey{
		ID: "bootstrap", Identity: platformID, Scope: auth.ScopeAll,
		Role: auth.RoleAdmin, Platform: true,
	})
	return api
}

func TestPlatformOrgLifecycleOverHTTP(t *testing.T) {
	api := startTenancy(t)

	// Create an organization; the response carries the one-time org-admin secret.
	var created struct {
		OrgKey         string `json:"org_key"`
		AdminKeyID     string `json:"admin_key_id"`
		AdminKeySecret string `json:"admin_key_secret"`
	}
	api.Request(t, "POST", "/v1/platform/orgs", map[string]any{
		"key": "acme", "display": "Acme Corp",
		"config":      map[string]any{"plan": "enterprise", "max_workspaces": 5},
		"admin_actor": "alice",
	}, 201, &created)
	if created.OrgKey != "acme" || created.AdminKeySecret == "" {
		t.Fatalf("create org = %+v, want org key + admin secret", created)
	}

	// A non-platform principal cannot create organizations.
	member := api.AddKey("member-key", auth.APIKey{
		ID: "member", Identity: identity.Identity{Org: "default", Workspace: "main", Actor: "member"},
		Scope: auth.ScopeAll, Role: auth.RoleAdmin, Platform: false,
	})
	member.Request(t, "POST", "/v1/platform/orgs", map[string]any{
		"key": "other", "display": "Other", "admin_actor": "x",
	}, 403, nil)

	// The org-admin key authenticates as admin in the new org and manages workspaces.
	orgAdmin := api.AddKey(created.AdminKeySecret, auth.APIKey{
		ID:       created.AdminKeyID,
		Identity: identity.Identity{Org: "acme", Workspace: "main", Actor: "alice"},
		Scope:    auth.ScopeAll, Role: auth.RoleAdmin, Platform: false,
	})
	orgAdmin.Request(t, "POST", "/v1/orgs/acme/workspaces", map[string]any{
		"key": "west", "display": "West", "config": map[string]any{"retention_days": 90},
	}, 202, nil)

	var spaces struct {
		Workspaces []struct {
			Key    string `json:"key"`
			Status string `json:"status"`
		} `json:"workspaces"`
	}
	orgAdmin.Request(t, "GET", "/v1/orgs/acme/workspaces", nil, 200, &spaces)
	if len(spaces.Workspaces) != 2 {
		t.Fatalf("workspaces = %v, want main + west", spaces.Workspaces)
	}

	// Workspace administration is scoped to the caller's own org: a non-platform
	// admin of the default org cannot touch acme's workspaces. (A platform
	// principal — the seeded harness key — deliberately can.)
	otherOrgAdmin := api.AddKey("other-org-admin", auth.APIKey{
		ID: "other", Identity: identity.Identity{Org: "default", Workspace: "main", Actor: "mallory"},
		Scope: auth.ScopeAll, Role: auth.RoleAdmin, Platform: false,
	})
	otherOrgAdmin.Request(t, "POST", "/v1/orgs/acme/workspaces", map[string]any{
		"key": "hijack", "display": "Hijack",
	}, 403, nil)

	// Membership: grant an editor, then revoke. The last admin is protected.
	orgAdmin.Request(t, "POST", "/v1/orgs/acme/workspaces/main/memberships", map[string]any{
		"actor": "carol", "role": "editor",
	}, 202, nil)
	orgAdmin.Request(t, "POST", "/v1/orgs/acme/workspaces/main/memberships/alice/revoke",
		map[string]any{"reason": "last admin must be refused"}, 400, nil)

	// Suspend and delete flow through the platform principal.
	api.Request(t, "POST", "/v1/platform/orgs/acme/suspend", map[string]any{"reason": "audit"}, 202, nil)
	api.Request(t, "POST", "/v1/platform/orgs/acme/resume", nil, 200, nil)
	api.Request(t, "POST", "/v1/platform/orgs/acme/delete", map[string]any{"reason": "closed"}, 400, nil)

	var org struct {
		Status string `json:"status"`
	}
	api.Request(t, "GET", "/v1/platform/orgs/acme", nil, 200, &org)
	if org.Status != "active" {
		t.Fatalf("org status = %q, want active", org.Status)
	}
}
