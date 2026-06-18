// SPDX-License-Identifier: AGPL-3.0-or-later

package erasure

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

func TestVaultSealOpenAndShred(t *testing.T) {
	ctx := context.Background()
	v := NewVault(store.NewMemory())
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "admin"}

	sealed, err := v.Seal(ctx, id, "subject-1", []byte("ada's ssn"))
	if err != nil {
		t.Fatal(err)
	}
	if string(sealed) == "ada's ssn" {
		t.Fatal("sealed value must not be plaintext")
	}
	plain, err := v.Open(ctx, id, "subject-1", sealed)
	if err != nil || string(plain) != "ada's ssn" {
		t.Fatalf("open = %q err=%v", plain, err)
	}
	if erased, _ := v.Erased(ctx, id, "subject-1"); erased {
		t.Fatal("subject should not be erased yet")
	}

	// Erase shreds the key: previously-sealed data is unrecoverable, and the
	// subject accepts no new data.
	if err := v.Erase(ctx, id, "subject-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Open(ctx, id, "subject-1", sealed); !errors.Is(err, ErrErased) {
		t.Fatalf("open after erase = %v, want ErrErased", err)
	}
	if _, err := v.Seal(ctx, id, "subject-1", []byte("more")); !errors.Is(err, ErrErased) {
		t.Fatalf("seal after erase = %v, want ErrErased", err)
	}
	if erased, _ := v.Erased(ctx, id, "subject-1"); !erased {
		t.Fatal("subject should be erased")
	}
	list, _ := v.ListErased(ctx, id)
	if len(list) != 1 || list[0] != "subject-1" {
		t.Fatalf("ListErased = %v", list)
	}

	// Erasing a never-sealed subject pre-emptively still blocks later sealing.
	if err := v.Erase(ctx, id, "ghost"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Seal(ctx, id, "ghost", []byte("x")); !errors.Is(err, ErrErased) {
		t.Fatalf("seal of pre-erased subject = %v, want ErrErased", err)
	}
}

func TestVaultRetentionSweep(t *testing.T) {
	ctx := context.Background()
	v := NewVault(store.NewMemory())
	clock := time.Now().UTC()
	v.now = func() time.Time { return clock }
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "admin"}

	if _, err := v.Seal(ctx, id, "old", []byte("a")); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(48 * time.Hour)
	if _, err := v.Seal(ctx, id, "new", []byte("b")); err != nil {
		t.Fatal(err)
	}

	// Sweep at +48h with a 24h limit erases "old" (created at T0) but not "new".
	n, err := v.RetentionSweep(ctx, id, 24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("sweep erased %d err=%v, want 1", n, err)
	}
	if erased, _ := v.Erased(ctx, id, "old"); !erased {
		t.Fatal("old subject should be erased by retention")
	}
	if erased, _ := v.Erased(ctx, id, "new"); erased {
		t.Fatal("new subject should survive retention")
	}
}
