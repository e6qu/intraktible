// SPDX-License-Identifier: AGPL-3.0-or-later

package store_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/e6qu/intraktible/platform/store"
)

type counter struct {
	N int `json:"n"`
}

func TestUpdateDocReturnsFalseOnMissingKey(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	ok, err := store.UpdateDoc[counter](ctx, s, "c", "absent", func(c *counter) { c.N++ })
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("UpdateDoc should report false (no write) for an absent key")
	}
}

// TestUpdateDocAtomicUnderConcurrency proves the transactional store serializes
// the read-modify-write: N concurrent increments all land (none lost to a
// read-read-write-write interleave).
func TestUpdateDocAtomicUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	s, err := store.NewSQLite(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := store.PutDoc[counter](ctx, s, "c", "k", counter{}); err != nil {
		t.Fatal(err)
	}

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.UpdateDoc[counter](ctx, s, "c", "k", func(c *counter) { c.N++ }); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent update: %v", err)
	}

	got, ok, err := store.GetDoc[counter](ctx, s, "c", "k")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.N != n {
		t.Fatalf("lost updates: got %d, want %d", got.N, n)
	}
}
