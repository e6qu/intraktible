// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !js

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

// TestPostgresListKeyPrefix exercises the non-empty-prefix range scan against a real
// Postgres — the path that regressed when the byte-successor upper bound met a
// linguistic default collation (notifications/comments keys contain ':' and tenant
// keys contain '/'). With COLLATE "C" the range must return exactly the prefixed
// rows regardless of the database's default collation.
func TestPostgresListKeyPrefix(t *testing.T) {
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
	if err := s.Reset(ctx, "pfx"); err != nil {
		t.Fatal(err)
	}
	keys := []string{
		"orgA/main/alice:c1", "orgA/main/alice:c2", "orgA/main/bob:c9",
		"orgB/main/eve:c1", "type:id:cmt1", "type:id:cmt2",
	}
	for _, k := range keys {
		if err := s.Put(ctx, "pfx", k, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		prefix string
		want   int
	}{
		{"orgA/main/", 3},       // tenant scope: '/'-terminated prefix
		{"orgA/main/alice:", 2}, // ':'-terminated (the notifications/comments shape)
		{"type:id:", 2},         // colon-heavy keys
		{"orgB/main/", 1},
		{"nope/", 0},
	} {
		got, err := s.List(ctx, "pfx", c.prefix)
		if err != nil {
			t.Fatalf("List(%q): %v", c.prefix, err)
		}
		if len(got) != c.want {
			t.Fatalf("List(%q) = %d rows, want %d (collation range bug?)", c.prefix, len(got), c.want)
		}
	}
}
