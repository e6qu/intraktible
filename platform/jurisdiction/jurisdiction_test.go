// SPDX-License-Identifier: AGPL-3.0-or-later

package jurisdiction_test

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/jurisdiction"
	"github.com/e6qu/intraktible/platform/store"
)

var (
	ctx = context.Background()
	id  = identity.Identity{Org: "o", Workspace: "w", Actor: "ava"}
	t0  = time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
)

func build(t *testing.T, log eventlog.Log) store.Store {
	t.Helper()
	st := store.NewMemory()
	evs, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if err := (jurisdiction.Projector{}).Apply(ctx, e, st); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestUnsetDefaultsToAll(t *testing.T) {
	st := store.NewMemory()
	got, err := jurisdiction.Applicable(ctx, st, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("unset should default to all three regimes, got %v", got)
	}
}

func TestSetAndRead(t *testing.T) {
	log := eventlog.NewMemory()
	h := jurisdiction.NewHandler(log).WithNow(func() time.Time { return t0 })
	// Deduplicates and normalizes case.
	if _, _, err := h.Set(ctx, id, []string{"EU", "us", "us"}); err != nil {
		t.Fatal(err)
	}
	st := build(t, log)
	got, _ := jurisdiction.Applicable(ctx, st, id)
	if len(got) != 2 || got[0] != "eu" || got[1] != "us" {
		t.Fatalf("applicable = %v, want [eu us]", got)
	}
	v, ok, _ := jurisdiction.Read(ctx, st, id)
	if !ok || v.UpdatedBy != "ava" {
		t.Fatalf("view = %+v", v)
	}
}

func TestSetValidation(t *testing.T) {
	h := jurisdiction.NewHandler(eventlog.NewMemory()).WithNow(func() time.Time { return t0 })
	if _, _, err := h.Set(ctx, id, []string{"eu", "atlantis"}); err == nil {
		t.Error("an unknown regime should be rejected")
	}
	if _, _, err := h.Set(ctx, id, nil); err == nil {
		t.Error("an empty regime set should be rejected")
	}
}
