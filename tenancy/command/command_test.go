// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/tenancy/domain"
)

func setup(t *testing.T) (*Handler, identity.Identity) {
	t.Helper()
	log := eventlog.NewMemory()
	h := NewHandler(log).WithNow(func() time.Time {
		return time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	})
	return h, identity.Identity{Org: "default", Workspace: "main", Actor: "bootstrap"}
}

func TestOrganizationLifecycleAndQuota(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, id := setup(t)

	// Create an org with a workspace quota of 1 (only the default "main").
	if _, err := h.CreateOrganization(ctx, id, "acme", "Acme Corp",
		domain.OrganizationConfig{Plan: "enterprise", MaxWorkspaces: 1}, "alice"); err != nil {
		t.Fatal(err)
	}

	// Duplicate creation is rejected.
	if _, err := h.CreateOrganization(ctx, id, "acme", "Acme Corp",
		domain.OrganizationConfig{}, "alice"); err == nil {
		t.Fatal("duplicate organization must be rejected")
	}

	// The workspace quota is exhausted (default "main" already occupies it).
	if _, err := h.CreateWorkspace(ctx, id, "acme", "west", "West", domain.WorkspaceConfig{}); err == nil ||
		!strings.Contains(err.Error(), "quota") {
		t.Fatalf("workspace quota must be enforced, got %v", err)
	}

	// Relax the quota and create a workspace.
	if _, err := h.ConfigureOrganization(ctx, id, "acme",
		domain.OrganizationConfig{Plan: "enterprise", MaxWorkspaces: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.CreateWorkspace(ctx, id, "acme", "west", "West", domain.WorkspaceConfig{}); err != nil {
		t.Fatal(err)
	}

	// Suspend the org: no new workspaces may be created.
	if _, err := h.SuspendOrganization(ctx, id, "acme", "non-payment"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.CreateWorkspace(ctx, id, "acme", "east", "East", domain.WorkspaceConfig{}); err == nil ||
		!strings.Contains(err.Error(), "suspended") {
		t.Fatalf("suspended org must not create workspaces, got %v", err)
	}
	if _, err := h.ResumeOrganization(ctx, id, "acme"); err != nil {
		t.Fatal(err)
	}

	// Deletion is blocked while active workspaces exist.
	if _, err := h.DeleteOrganization(ctx, id, "acme", "closed"); err == nil ||
		!strings.Contains(err.Error(), "active workspace") {
		t.Fatalf("deletion must be blocked by active workspaces, got %v", err)
	}

	// Delete the workspaces, then the org.
	if _, err := h.DeleteWorkspace(ctx, id, "acme", "west", "closed"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DeleteWorkspace(ctx, id, "acme", "main", "closed"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DeleteOrganization(ctx, id, "acme", "closed"); err != nil {
		t.Fatal(err)
	}

	// A deleted org cannot be configured or re-created against its live state.
	if _, err := h.ConfigureOrganization(ctx, id, "acme", domain.OrganizationConfig{}); err == nil {
		t.Fatal("a deleted org must not be configurable")
	}
}

func TestMembershipLifecycleAndLastAdminSafety(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, id := setup(t)
	if _, err := h.CreateOrganization(ctx, id, "beta", "Beta", domain.OrganizationConfig{}, "alice"); err != nil {
		t.Fatal(err)
	}

	// Alice is the org's initial admin. Grant a second admin and an editor.
	if _, err := h.GrantMembership(ctx, id, "beta", "main", "bob", domain.MembershipAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := h.GrantMembership(ctx, id, "beta", "main", "carol", domain.MembershipEditor); err != nil {
		t.Fatal(err)
	}
	// Duplicate active membership is rejected.
	if _, err := h.GrantMembership(ctx, id, "beta", "main", "carol", domain.MembershipEditor); err == nil {
		t.Fatal("duplicate active membership must be rejected")
	}

	// Revoke one admin; the other remains, so it is allowed.
	if _, err := h.RevokeMembership(ctx, id, "beta", "main", "bob", "rotation"); err != nil {
		t.Fatal(err)
	}
	// Carol is not an admin; revoking her leaves alice as the sole admin.
	if _, err := h.RevokeMembership(ctx, id, "beta", "main", "carol", "offboard"); err != nil {
		t.Fatal(err)
	}
	// Revoking the last active admin is refused.
	if _, err := h.RevokeMembership(ctx, id, "beta", "main", "alice", "offboard"); err == nil ||
		!strings.Contains(err.Error(), "last active admin") {
		t.Fatalf("last-admin revocation must be refused, got %v", err)
	}
}
