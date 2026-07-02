// SPDX-License-Identifier: AGPL-3.0-or-later

package erasure

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
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

func TestSealAndOpenFields(t *testing.T) {
	ctx := context.Background()
	v := NewVault(store.NewMemory())
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "admin"}
	doc := []byte(`{"ssn":"123-45-6789","amount":100,"note":"hi"}`)
	fields := map[string]bool{"ssn": true}

	sealed, err := v.SealFields(ctx, id, "ada", doc, fields)
	if err != nil {
		t.Fatal(err)
	}
	// The PII field is sealed; the non-PII fields (e.g. the feature-engine numeric
	// "amount") stay in plaintext.
	if got := string(sealed); strings.Contains(got, "123-45-6789") || !strings.Contains(got, "$intraktible_erased") ||
		!strings.Contains(got, "100") || !strings.Contains(got, "hi") {
		t.Fatalf("sealed = %s", got)
	}

	opened, err := v.OpenFields(ctx, id, "ada", sealed)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(opened, &m)
	if m["ssn"] != "123-45-6789" || m["amount"].(float64) != 100 || m["note"] != "hi" {
		t.Fatalf("opened = %s", opened)
	}

	// After erasure the sealed field reads "[erased]" while the rest survives.
	if err := v.Erase(ctx, id, "ada"); err != nil {
		t.Fatal(err)
	}
	opened, err = v.OpenFields(ctx, id, "ada", sealed)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(opened, &m)
	if m["ssn"] != "[erased]" || m["amount"].(float64) != 100 {
		t.Fatalf("opened after erase = %s", opened)
	}
	// Sealing new data for an erased subject is refused.
	if _, err := v.SealFields(ctx, id, "ada", doc, fields); !errors.Is(err, ErrErased) {
		t.Fatalf("seal fields for erased subject = %v, want ErrErased", err)
	}
}

// TestSealFieldsNestedAndCaseInsensitive guards the read/write symmetry with
// privacy.Mask: sealing must reach nested objects and arrays and match field
// names case-insensitively, or PII the read boundary masks would stay in the
// clear in the log and survive erasure.
func TestSealFieldsNestedAndCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	v := NewVault(store.NewMemory())
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "admin"}
	doc := []byte(`{"customer":{"SSN":"123-45-6789","name":"ada"},"contacts":[{"email":"a@b.c"},{"email":"d@e.f"}],"amount":100}`)
	fields := map[string]bool{"ssn": true, "EMAIL": true}

	sealed, err := v.SealFields(ctx, id, "ada", doc, fields)
	if err != nil {
		t.Fatal(err)
	}
	got := string(sealed)
	if strings.Contains(got, "123-45-6789") || strings.Contains(got, "a@b.c") || strings.Contains(got, "d@e.f") {
		t.Fatalf("nested/case-mismatched PII left in clear: %s", got)
	}
	if !strings.Contains(got, "ada") || !strings.Contains(got, "100") {
		t.Fatalf("non-PII fields should survive: %s", got)
	}

	opened, err := v.OpenFields(ctx, id, "ada", sealed)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(opened, &m); err != nil {
		t.Fatal(err)
	}
	cust := m["customer"].(map[string]any)
	if cust["SSN"] != "123-45-6789" || cust["name"] != "ada" {
		t.Fatalf("nested open mismatch: %s", opened)
	}
	contacts := m["contacts"].([]any)
	if contacts[0].(map[string]any)["email"] != "a@b.c" || contacts[1].(map[string]any)["email"] != "d@e.f" {
		t.Fatalf("array open mismatch: %s", opened)
	}

	if err := v.Erase(ctx, id, "ada"); err != nil {
		t.Fatal(err)
	}
	opened, err = v.OpenFields(ctx, id, "ada", sealed)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(opened, &m)
	if m["customer"].(map[string]any)["SSN"] != "[erased]" {
		t.Fatalf("nested erase mismatch: %s", opened)
	}
	if m["contacts"].([]any)[0].(map[string]any)["email"] != "[erased]" {
		t.Fatalf("array erase mismatch: %s", opened)
	}
}

// TestVaultSealConcurrentFirstUse guards the first-use key creation race: many
// concurrent first seals of one subject must agree on a single key, or the
// losers' envelopes would be permanently undecryptable. Covers both the
// mutex-serialized in-memory path and the transactional (SQLite) path.
func TestVaultSealConcurrentFirstUse(t *testing.T) {
	sqlite, err := store.NewSQLite(filepath.Join(t.TempDir(), "erasure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlite.Close() }()
	backends := map[string]store.Store{"memory": store.NewMemory(), "sqlite": sqlite}
	for name, st := range backends {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			v := NewVault(st)
			id := identity.Identity{Org: "o", Workspace: "w", Actor: "admin"}
			const n = 32
			sealed := make([][]byte, n)
			errs := make([]error, n)
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					sealed[i], errs[i] = v.Seal(ctx, id, "fresh", []byte("ada's pii"))
				}(i)
			}
			wg.Wait()
			for i := 0; i < n; i++ {
				if errs[i] != nil {
					t.Fatalf("seal %d: %v", i, errs[i])
				}
				plain, err := v.Open(ctx, id, "fresh", sealed[i])
				if err != nil || string(plain) != "ada's pii" {
					t.Fatalf("open envelope %d = %q err=%v — sealed under a losing key", i, plain, err)
				}
			}
		})
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
