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
	list, err := m.List(ctx, "c")
	if err != nil || len(list) != 2 || list[0].Key != "k1" {
		t.Fatalf("List = %+v err=%v", list, err)
	}
	if err := m.Reset(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if list, _ := m.List(ctx, "c"); len(list) != 0 {
		t.Fatalf("after Reset List = %d, want 0", len(list))
	}
}

func TestKey(t *testing.T) {
	if got := Key("o", "w", "id"); got != "o/w/id" {
		t.Fatalf("Key = %q", got)
	}
}
