// SPDX-License-Identifier: AGPL-3.0-or-later

package projection_test

import (
	"context"
	"os"
	"testing"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/tenancy/command"
	"github.com/e6qu/intraktible/tenancy/domain"
	tenancyprojection "github.com/e6qu/intraktible/tenancy/projection"
)

// TestTenancyProjectionBootstrapOnPostgres proves the tenancy read models
// bootstrap and apply over a REAL Postgres store — the path the production SSO
// job exercises. It guards against reintroducing a store key that Postgres
// text columns cannot hold (e.g. a NUL byte separator). Skipped without a DSN.
func TestTenancyProjectionBootstrapOnPostgres(t *testing.T) {
	dsn := os.Getenv("INTRAKTIBLE_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set INTRAKTIBLE_TEST_POSTGRES (a pgx DSN) to run the Postgres tenancy test")
	}
	ctx := context.Background()
	st, err := store.NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	for _, collection := range (tenancyprojection.Projector{}).Collections() {
		if err := st.Reset(ctx, collection); err != nil {
			t.Fatal(err)
		}
	}

	log := eventlog.NewMemory()
	bootstrap := identity.Identity{Org: "default", Workspace: "main", Actor: "bootstrap"}
	handler := command.NewHandler(log)
	if _, err := handler.CreateOrganization(
		ctx, bootstrap, "acme", "Acme Corp",
		domain.OrganizationConfig{Plan: "enterprise", MaxWorkspaces: 5}, "alice",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.CreateWorkspace(
		ctx, bootstrap, "acme", "west", "West", domain.WorkspaceConfig{},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.GrantMembership(
		ctx, bootstrap, "acme", "west", "carol", domain.MembershipEditor,
	); err != nil {
		t.Fatal(err)
	}

	rt := projection.New(log, st, tenancyprojection.Projector{})
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("projection bootstrap on Postgres: %v", err)
	}

	org, found, err := store.GetDoc[tenancyprojection.OrganizationView](
		ctx, st, tenancyprojection.CollectionOrgs, tenancyprojection.OrgKey("acme"),
	)
	if err != nil || !found {
		t.Fatalf("read org: found=%v err=%v", found, err)
	}
	if org.Status != domain.OrganizationActive {
		t.Fatalf("org status = %q, want active", org.Status)
	}
	ws, found, err := store.GetDoc[tenancyprojection.WorkspaceView](
		ctx, st, tenancyprojection.CollectionWorkspaces, tenancyprojection.WorkspaceKey("acme", "west"),
	)
	if err != nil || !found || ws.Display != "West" {
		t.Fatalf("read workspace: found=%v err=%v ws=%+v", found, err, ws)
	}
	member, found, err := store.GetDoc[tenancyprojection.MembershipView](
		ctx, st, tenancyprojection.CollectionMemberships,
		tenancyprojection.MembershipKey("acme", "west", "carol"),
	)
	if err != nil || !found || member.Role != domain.MembershipEditor {
		t.Fatalf("read membership: found=%v err=%v member=%+v", found, err, member)
	}
}
