// SPDX-License-Identifier: AGPL-3.0-or-later

package store_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/e6qu/intraktible/platform/store"
)

// TestPostgresStore runs the full Store contract against a real PostgreSQL,
// pointed to by INTRAKTIBLE_TEST_POSTGRES (a pgx DSN). It is skipped otherwise —
// the default CI here has no Postgres (see BUGS.md D21). To run it:
//
//	docker compose -f deploy/docker-compose.yml --profile pg up -d postgres
//	INTRAKTIBLE_TEST_POSTGRES='postgres://postgres:intraktible@localhost:5432/intraktible' go test ./platform/store/
func TestPostgresStore(t *testing.T) {
	dsn := os.Getenv("INTRAKTIBLE_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set INTRAKTIBLE_TEST_POSTGRES (a pgx DSN) to run the Postgres store test")
	}
	ctx := context.Background()
	s, err := store.NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	// Start from a clean collection (the table persists across runs).
	if err := s.Reset(ctx, "c"); err != nil {
		t.Fatal(err)
	}

	if _, ok, _ := s.Get(ctx, "c", "missing"); ok {
		t.Fatal("missing key should not be found")
	}
	if err := s.Put(ctx, "c", "k2", json.RawMessage(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "c", "k1", json.RawMessage(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	// Upsert overwrites k1.
	if err := s.Put(ctx, "c", "k1", json.RawMessage(`{"v":11}`)); err != nil {
		t.Fatal(err)
	}

	doc, ok, err := s.Get(ctx, "c", "k1")
	if err != nil || !ok {
		t.Fatalf("Get k1 ok=%v err=%v", ok, err)
	}
	var got struct{ V int }
	if err := json.Unmarshal(doc, &got); err != nil || got.V != 11 {
		t.Fatalf("Get k1 = %s (v=%d) err=%v", doc, got.V, err)
	}

	list, err := s.List(ctx, "c")
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
	if l, _ := s.List(ctx, "c"); len(l) != 0 {
		t.Fatalf("after Reset List = %d, want 0", len(l))
	}
}
