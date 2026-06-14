// SPDX-License-Identifier: AGPL-3.0-or-later

package hello_test

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/intraktible/hello/command"
	"github.com/e6qu/intraktible/hello/domain"
	"github.com/e6qu/intraktible/hello/stats"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func TestHelloSliceReplay(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()

	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "tester"}
	h := command.NewHandler(log)
	if _, err := h.SayHello(ctx, id, domain.SayHello{Name: "ada"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SayHello(ctx, id, domain.SayHello{Name: "alan"}); err != nil {
		t.Fatal(err)
	}

	// Start replays existing events synchronously before returning.
	st := store.NewMemory()
	rt := projection.New(log, st, stats.Projector{})
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := stats.Read(ctx, st, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 2 || got.LastName != "alan" {
		t.Fatalf("after replay: count=%d last=%q, want 2/alan", got.Count, got.LastName)
	}

	// Live path: a new command should reach the projection via the bus.
	if _, err := h.SayHello(ctx, id, domain.SayHello{Name: "grace"}); err != nil {
		t.Fatal(err)
	}
	if !eventually(t, func() bool {
		s, _ := stats.Read(ctx, st, id)
		return s.Count == 3 && s.LastName == "grace"
	}) {
		t.Fatal("live projection did not reach count=3/grace")
	}
}

func TestHelloTenantIsolation(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	h := command.NewHandler(log)
	a := identity.Identity{Org: "a", Workspace: "main", Actor: "x"}
	b := identity.Identity{Org: "b", Workspace: "main", Actor: "y"}
	_, _ = h.SayHello(ctx, a, domain.SayHello{Name: "one"})
	_, _ = h.SayHello(ctx, a, domain.SayHello{Name: "two"})
	_, _ = h.SayHello(ctx, b, domain.SayHello{Name: "three"})

	st := store.NewMemory()
	if err := projection.New(log, st, stats.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	sa, _ := stats.Read(ctx, st, a)
	sb, _ := stats.Read(ctx, st, b)
	if sa.Count != 2 || sb.Count != 1 {
		t.Fatalf("tenant isolation: a=%d b=%d, want 2/1", sa.Count, sb.Count)
	}
}

func TestSayHelloValidation(t *testing.T) {
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	h := command.NewHandler(log)
	if _, err := h.SayHello(context.Background(),
		identity.Identity{Org: "o", Workspace: "w", Actor: "a"},
		domain.SayHello{Name: "  "}); err == nil {
		t.Fatal("expected validation error for blank name")
	}
}

func eventually(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
