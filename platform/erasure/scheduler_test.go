// SPDX-License-Identifier: AGPL-3.0-or-later

package erasure

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

type subjectRetentionGate struct {
	retained map[string]bool
}

func (g subjectRetentionGate) Retained(_ context.Context, _ identity.Identity, subject string) (bool, string, error) {
	return g.retained[subject], "statutory test hold", nil
}

func TestSchedulerPreservesHeldAndStatutorilyRetainedSubjects(t *testing.T) {
	ctx := context.Background()
	vault := NewVault(store.NewMemory())
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "admin"}
	created := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	vault.now = func() time.Time { return created }
	for _, subject := range []string{"erase", "held", "statutory"} {
		if _, err := vault.Seal(ctx, id, subject, []byte("pii")); err != nil {
			t.Fatal(err)
		}
	}
	if err := vault.Hold(ctx, id, "held", "litigation"); err != nil {
		t.Fatal(err)
	}
	if err := vault.SetRetentionPolicy(ctx, id, 30); err != nil {
		t.Fatal(err)
	}

	now := created.Add(365 * 24 * time.Hour)
	scheduler := NewScheduler(vault).
		WithNow(func() time.Time { return now }).
		WithRetentionGate(subjectRetentionGate{retained: map[string]bool{"statutory": true}})
	vault.now = func() time.Time { return now }
	summary, err := scheduler.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Erased != 1 || summary.Held != 1 || summary.StatutoryRetained != 1 {
		t.Fatalf("scheduler summary = %+v, want one outcome in each bucket", summary)
	}
	for subject, wantErased := range map[string]bool{"erase": true, "held": false, "statutory": false} {
		got, err := vault.Erased(ctx, id, subject)
		if err != nil || got != wantErased {
			t.Fatalf("%s erased=%v err=%v, want %v", subject, got, err, wantErased)
		}
	}
}
