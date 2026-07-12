// SPDX-License-Identifier: AGPL-3.0-or-later

package sharing_test

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/sharing"
	"github.com/e6qu/intraktible/platform/store"
)

var (
	ctx = context.Background()
	id  = identity.Identity{Org: "o", Workspace: "w", Actor: "diego"}
	t0  = time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
)

func build(t *testing.T, log eventlog.Log) store.Store {
	t.Helper()
	st := store.NewMemory()
	evs, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if err := (sharing.Projector{}).Apply(ctx, e, st); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func handler(now time.Time) (*sharing.Handler, eventlog.Log) {
	log := eventlog.NewMemory()
	return sharing.NewHandler(log).WithNow(func() time.Time { return now }), log
}

func TestOptOutAndRescind(t *testing.T) {
	h, log := handler(t0)
	if _, err := h.OptOut(ctx, id, "applicant/APP-1", "customer request"); err != nil {
		t.Fatal(err)
	}
	st := build(t, log)
	if out, _ := sharing.HasOptedOut(ctx, st, id, "applicant/APP-1"); !out {
		t.Fatal("expected the subject to be opted out")
	}
	rec, found, _ := sharing.Get(ctx, st, id, "applicant/APP-1")
	if !found || !rec.OptedOut || rec.Reason != "customer request" || rec.UpdatedBy != "diego" {
		t.Fatalf("record = %+v", rec)
	}

	// Rescinding opts the subject back in.
	if _, err := h.Rescind(ctx, id, "applicant/APP-1"); err != nil {
		t.Fatal(err)
	}
	st = build(t, log)
	if out, _ := sharing.HasOptedOut(ctx, st, id, "applicant/APP-1"); out {
		t.Fatal("expected the subject to be opted back in after rescind")
	}
}

func TestOptOutValidation(t *testing.T) {
	h, _ := handler(t0)
	if _, err := h.OptOut(ctx, id, "  ", ""); err == nil {
		t.Error("an empty subject should fail")
	}
}

func TestListAll(t *testing.T) {
	h, log := handler(t0)
	h.OptOut(ctx, id, "applicant/APP-1", "")
	h.OptOut(ctx, id, "applicant/APP-2", "")
	st := build(t, log)
	all, err := sharing.ListAll(ctx, st, id)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListAll = %v (%v)", all, err)
	}
}
