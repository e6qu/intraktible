// SPDX-License-Identifier: AGPL-3.0-or-later

package store_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/e6qu/intraktible/platform/store"
)

func TestSQLitePutGetListResetDelete(t *testing.T) {
	ctx := context.Background()
	s, err := store.NewSQLite(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	if _, ok, _ := s.Get(ctx, "c", "missing"); ok {
		t.Fatal("missing key should not be found")
	}
	if err := s.Put(ctx, "c", "k2", json.RawMessage(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "c", "k1", json.RawMessage(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	// Upsert (overwrite k1).
	if err := s.Put(ctx, "c", "k1", json.RawMessage(`{"v":11}`)); err != nil {
		t.Fatal(err)
	}

	doc, ok, err := s.Get(ctx, "c", "k1")
	if err != nil || !ok || string(doc) != `{"v":11}` {
		t.Fatalf("Get k1 = %s ok=%v err=%v", doc, ok, err)
	}
	list, err := s.List(ctx, "c", "")
	if err != nil || len(list) != 2 || list[0].Key != "k1" || list[1].Key != "k2" {
		t.Fatalf("List = %+v err=%v", list, err)
	}
	if err := s.Delete(ctx, "c", "k1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, "c", "k1"); ok {
		t.Fatal("k1 should be deleted")
	}
	if err := s.Reset(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.List(ctx, "c", ""); len(l) != 0 {
		t.Fatalf("after Reset List = %d, want 0", len(l))
	}
}

// TestSQLiteDurability is the point of D2: data survives closing and reopening the
// same file (a process restart).
func TestSQLiteDurability(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "p.db")

	s1, err := store.NewSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Put(ctx, "cases", "demo/main/c1", json.RawMessage(`{"status":"open"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := store.NewSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	doc, ok, err := s2.Get(ctx, "cases", "demo/main/c1")
	if err != nil || !ok || string(doc) != `{"status":"open"}` {
		t.Fatalf("after reopen Get = %s ok=%v err=%v", doc, ok, err)
	}
}
