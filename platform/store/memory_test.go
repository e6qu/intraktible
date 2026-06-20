// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMemoryPutGetListReset(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()

	if _, ok, err := m.Get(ctx, "c", "missing"); err != nil || ok {
		t.Fatalf("Get missing: ok=%v err=%v", ok, err)
	}
	if err := m.Put(ctx, "c", "k1", json.RawMessage(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := m.Put(ctx, "c", "k2", json.RawMessage(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	doc, ok, err := m.Get(ctx, "c", "k1")
	if err != nil || !ok || string(doc) != `{"v":1}` {
		t.Fatalf("Get k1 = %s ok=%v err=%v", doc, ok, err)
	}
	list, err := m.List(ctx, "c", "")
	if err != nil || len(list) != 2 || list[0].Key != "k1" {
		t.Fatalf("List = %+v err=%v", list, err)
	}
	if err := m.Reset(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if list, _ := m.List(ctx, "c", ""); len(list) != 0 {
		t.Fatalf("after Reset List = %d, want 0", len(list))
	}
}

// List with a key prefix returns only the matching keys (the tenant-scoping path),
// not the whole collection — and an empty prefix still returns everything.
func TestMemoryListKeyPrefix(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	for _, k := range []string{"orgA/main/x", "orgA/main/y", "orgB/main/z"} {
		if err := m.Put(ctx, "c", k, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.List(ctx, "c", "orgA/main/")
	if err != nil || len(got) != 2 {
		t.Fatalf("prefix list = %d docs err=%v, want 2", len(got), err)
	}
	for _, r := range got {
		if r.Key == "orgB/main/z" {
			t.Fatal("prefix list leaked a non-matching key")
		}
	}
	if all, _ := m.List(ctx, "c", ""); len(all) != 3 {
		t.Fatalf("empty prefix should list all 3, got %d", len(all))
	}
}

func TestKey(t *testing.T) {
	if got := Key("o", "w", "id"); got != "o/w/id" {
		t.Fatalf("Key = %q", got)
	}
}

func TestMemoryCollections(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	if got := m.Collections(); len(got) != 0 {
		t.Fatalf("empty store collections = %v", got)
	}
	_ = m.Put(ctx, "beta", "k", json.RawMessage(`1`))
	_ = m.Put(ctx, "alpha", "k", json.RawMessage(`1`))
	// An emptied collection is not reported.
	_ = m.Put(ctx, "gone", "k", json.RawMessage(`1`))
	_ = m.Delete(ctx, "gone", "k")

	got := m.Collections()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("Collections = %v, want [alpha beta] sorted", got)
	}
}
