// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// White-box: drive the token clock to exercise rotation's grace window.
func TestRotateGraceWindow(t *testing.T) {
	ctx := context.Background()
	keys := NewStoreAPIKeys(store.NewMemory())
	clock := time.Now().UTC()
	keys.now = func() time.Time { return clock }
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "svc"}

	created, oldSecret, err := keys.Create(ctx, ManagedAPIKey{Name: "svc", Identity: id, Role: RoleOperator})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := keys.ResolveSecret(oldSecret); !ok {
		t.Fatal("a fresh secret should resolve")
	}

	// Rotate with a one-hour grace window: both secrets work during the window.
	_, newSecret, err := keys.Rotate(ctx, created.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := keys.ResolveSecret(newSecret); !ok {
		t.Fatal("the new secret should resolve after rotation")
	}
	if _, ok := keys.ResolveSecret(oldSecret); !ok {
		t.Fatal("the old secret should resolve within the grace window")
	}

	// Past the window: only the new secret works.
	clock = clock.Add(2 * time.Hour)
	if _, ok := keys.ResolveSecret(oldSecret); ok {
		t.Fatal("the old secret should not resolve after the grace window")
	}
	if _, ok := keys.ResolveSecret(newSecret); !ok {
		t.Fatal("the new secret should still resolve after the window")
	}

	// A zero-grace rotation invalidates the prior secret immediately.
	_, newest, err := keys.Rotate(ctx, created.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := keys.ResolveSecret(newSecret); ok {
		t.Fatal("a zero-grace rotation should kill the prior secret at once")
	}
	if _, ok := keys.ResolveSecret(newest); !ok {
		t.Fatal("the newest secret should resolve")
	}

	// Rotating a revoked token fails loudly.
	if _, err := keys.Revoke(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := keys.Rotate(ctx, created.ID, time.Hour); err == nil {
		t.Fatal("rotating a revoked token should error")
	}
}
