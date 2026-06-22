// SPDX-License-Identifier: AGPL-3.0-or-later

package grants_test

import (
	"context"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/grants"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestGrantEnforcement(t *testing.T) {
	ctx := context.Background()
	log, st := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "admin"}
	cmd := grants.NewHandler(log)
	proj := grants.Projector{}

	apply := func(e eventlog.Envelope) {
		if err := proj.Apply(ctx, e, st); err != nil {
			t.Fatal(err)
		}
	}

	// A flow with NO grants is allowed for anyone (backward compatible — global RBAC
	// already gated the request).
	if ok, _ := grants.Allowed(ctx, st, id, "f1", "production", "alice"); !ok {
		t.Fatal("a flow with no grants must be allowed")
	}

	// Grant alice production on f1.
	_, e, err := cmd.Add(ctx, id, "f1", "alice", "production")
	if err != nil {
		t.Fatal(err)
	}
	apply(e)

	// Now f1 is restricted: alice (production) yes, bob no, alice in sandbox no.
	if ok, _ := grants.Allowed(ctx, st, id, "f1", "production", "alice"); !ok {
		t.Fatal("alice should be allowed in production")
	}
	if ok, _ := grants.Allowed(ctx, st, id, "f1", "production", "bob"); ok {
		t.Fatal("bob has no grant — must be denied")
	}
	if ok, _ := grants.Allowed(ctx, st, id, "f1", "sandbox", "alice"); ok {
		t.Fatal("alice's production grant must not cover sandbox")
	}
	// A different flow is still unrestricted.
	if ok, _ := grants.Allowed(ctx, st, id, "f2", "production", "bob"); !ok {
		t.Fatal("f2 has no grants — must be allowed")
	}

	// A wildcard env grant covers every environment.
	_, e2, err := cmd.Add(ctx, id, "f1", "carol", "*")
	if err != nil {
		t.Fatal(err)
	}
	apply(e2)
	for _, env := range []string{"sandbox", "staging", "production"} {
		if ok, _ := grants.Allowed(ctx, st, id, "f1", env, "carol"); !ok {
			t.Fatalf("carol's wildcard grant should cover %s", env)
		}
	}

	// Revoking alice's grant denies her again.
	gid, e3, err := cmd.Add(ctx, id, "f1", "dave", "production")
	if err != nil {
		t.Fatal(err)
	}
	apply(e3)
	revoke, err := cmd.Revoke(ctx, id, "f1", gid)
	if err != nil {
		t.Fatal(err)
	}
	apply(revoke)
	if ok, _ := grants.Allowed(ctx, st, id, "f1", "production", "dave"); ok {
		t.Fatal("dave's grant was revoked — must be denied")
	}

	// An invalid environment is rejected at the command boundary.
	if _, _, err := cmd.Add(ctx, id, "f1", "eve", "banana"); err == nil {
		t.Fatal("expected an invalid-environment error")
	}

	// AllowedAny (env-agnostic actions like publish/cancel): any grant for the flow
	// suffices; a non-grantee is denied; an ungranted flow is open.
	if ok, _ := grants.AllowedAny(ctx, st, id, "f1", "alice"); !ok {
		t.Fatal("alice holds a production grant — AllowedAny should pass")
	}
	if ok, _ := grants.AllowedAny(ctx, st, id, "f1", "bob"); ok {
		t.Fatal("bob has no grant on f1 — AllowedAny should deny")
	}
	if ok, _ := grants.AllowedAny(ctx, st, id, "f2", "bob"); !ok {
		t.Fatal("f2 has no grants — AllowedAny should be open")
	}
}
