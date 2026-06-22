// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// A key written before the hash index existed (no index entry) still resolves —
// the one-time backfill indexes it on first use — and a revoked key is denied.
func TestResolveBackfillsAndRevokes(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	keys := NewStoreAPIKeys(st)
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "svc"}

	// Simulate a pre-index key: write the key doc directly, with NO index entry.
	const secret = "itk_legacy-secret"
	legacy := ManagedAPIKey{ID: "legacy", Name: "legacy", Identity: id, Scope: Sandbox, Role: RoleOperator, Hash: hash(secret)}
	if err := store.PutDoc(ctx, st, managedKeyCollection, legacy.ID, legacy); err != nil {
		t.Fatal(err)
	}
	got, ok := keys.ResolveSecret(secret)
	if !ok || got.ID != "legacy" {
		t.Fatalf("pre-index key should resolve via backfill: ok=%v id=%q", ok, got.ID)
	}
	// It is now indexed, so a second resolve takes the fast path (still correct).
	if _, ok := keys.ResolveSecret(secret); !ok {
		t.Fatal("indexed key should still resolve")
	}
	// A bogus secret never resolves.
	if _, ok := keys.ResolveSecret("itk_nope"); ok {
		t.Fatal("a bogus secret must not resolve")
	}
	// Revoking denies resolution even though the index entry remains.
	if _, err := keys.Revoke(ctx, "legacy"); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys.ResolveSecret(secret); ok {
		t.Fatal("a revoked key must not resolve")
	}
}

// indexRows counts the hash-index entries — white-box visibility into the leak
// the prune fixes (a stale row never authenticates, but it shouldn't accumulate).
func indexRows(t *testing.T, st store.Store) int {
	t.Helper()
	rows, err := store.ListDocs[keyIndexEntry](context.Background(), st, managedKeyIndexCollection, "")
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// Rotation and revocation prune the hash-index rows they retire, so the global
// index holds only rows that can still authenticate — it does not grow by one
// orphan per rotation over a key's lifetime.
func TestRotateAndRevokePruneIndex(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	keys := NewStoreAPIKeys(st)
	clock := time.Now().UTC()
	keys.now = func() time.Time { return clock }
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "svc"}

	created, _, err := keys.Create(ctx, ManagedAPIKey{Name: "svc", Identity: id, Role: RoleOperator})
	if err != nil {
		t.Fatal(err)
	}
	if n := indexRows(t, st); n != 1 {
		t.Fatalf("after create: %d index rows, want 1", n)
	}

	// A grace rotation keeps the previous hash live → two rows (current + previous).
	if _, _, err := keys.Rotate(ctx, created.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	if n := indexRows(t, st); n != 2 {
		t.Fatalf("after grace rotation: %d index rows, want 2", n)
	}

	// A second grace rotation retires the first rotation's previous hash → still two.
	if _, _, err := keys.Rotate(ctx, created.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	if n := indexRows(t, st); n != 2 {
		t.Fatalf("after second grace rotation: %d index rows, want 2 (no orphan accrual)", n)
	}

	// A zero-grace rotation drops the prior current + previous → one row.
	if _, _, err := keys.Rotate(ctx, created.ID, 0); err != nil {
		t.Fatal(err)
	}
	if n := indexRows(t, st); n != 1 {
		t.Fatalf("after zero-grace rotation: %d index rows, want 1", n)
	}

	// Revoking prunes the remaining row(s).
	if _, err := keys.Revoke(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if n := indexRows(t, st); n != 0 {
		t.Fatalf("after revoke: %d index rows, want 0", n)
	}
}

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
