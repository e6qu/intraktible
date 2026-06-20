// SPDX-License-Identifier: AGPL-3.0-or-later

package preapproval_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/decision-engine/preapproval"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestGrantActiveExpireRevoke(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	h := preapproval.NewHandler(log)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "ops"}
	ctx := context.Background()

	if _, _, err := h.Grant(ctx, id, preapproval.GrantCmd{
		EntityType: "applicant", EntityID: "acme", Disposition: string(policy.Approve),
		Terms: json.RawMessage(`{"limit":15000}`), PolicyID: "p1", PolicyVersion: 2, ValidDays: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := projection.New(log, st, preapproval.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	v, ok, err := preapproval.ActiveFor(ctx, st, id, "applicant", "acme", now)
	if err != nil || !ok {
		t.Fatalf("expected an active pre-approval: ok=%v err=%v", ok, err)
	}
	if v.Disposition != string(policy.Approve) || string(v.Terms) != `{"limit":15000}` || v.PolicyVersion != 2 {
		t.Fatalf("unexpected pre-approval: %+v", v)
	}

	// Expired beyond the 1-day window → not active.
	if _, ok, _ := preapproval.ActiveFor(ctx, st, id, "applicant", "acme", now.Add(48*time.Hour)); ok {
		t.Fatal("expired pre-approval should not be active")
	}

	// Revoking invalidates it before expiry.
	if _, err := h.Revoke(ctx, id, "applicant", "acme", "fraud flag"); err != nil {
		t.Fatal(err)
	}
	if _, err := projection.New(log, st, preapproval.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := preapproval.ActiveFor(ctx, st, id, "applicant", "acme", now); ok {
		t.Fatal("revoked pre-approval should not be active")
	}
}

func TestGrantValidation(t *testing.T) {
	log, _ := testutil.NewLogStore(t)
	h := preapproval.NewHandler(log)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "ops"}
	ctx := context.Background()
	bad := []preapproval.GrantCmd{
		{EntityID: "x", ValidDays: 1},                                          // missing type
		{EntityType: "t", ValidDays: 1},                                        // missing id
		{EntityType: "t", EntityID: "x", ValidDays: 0},                         // non-positive window
		{EntityType: "t", EntityID: "x", ValidDays: 1, Disposition: "perhaps"}, // bad disposition
	}
	for i, c := range bad {
		if _, _, err := h.Grant(ctx, id, c); err == nil {
			t.Fatalf("bad grant %d passed validation", i)
		}
	}
}
