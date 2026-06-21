// SPDX-License-Identifier: AGPL-3.0-or-later

package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

type fakeCmd struct {
	activated []string
	reverted  []string
	priors    map[string]int
}

func (f *fakeCmd) ActivateSchedule(_ context.Context, _ identity.Identity, scheduleID, _, _ string, _, priorVersion int) error {
	f.activated = append(f.activated, scheduleID)
	if f.priors == nil {
		f.priors = map[string]int{}
	}
	f.priors[scheduleID] = priorVersion
	return nil
}

func (f *fakeCmd) RevertSchedule(_ context.Context, _ identity.Identity, scheduleID, _, _ string, _ int) error {
	f.reverted = append(f.reverted, scheduleID)
	return nil
}

func seedSchedule(t *testing.T, s store.Store, v View) {
	t.Helper()
	v.Org, v.Workspace = "demo", "main"
	if err := store.PutDoc(context.Background(), s, Collection, store.Key("demo", "main", v.ScheduleID), v); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerActivatesDueAndCapturesPrior(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	// A flow with v3 currently live in sandbox — the prior version a time-box reverts to.
	if err := store.PutDoc(ctx, st, flows.Collection, store.Key("demo", "main", "f1"),
		flows.FlowView{FlowID: "f1", Deployments: map[string]flows.DeploymentView{"sandbox": {Version: 3}}}); err != nil {
		t.Fatal(err)
	}
	seedSchedule(t, st, View{ScheduleID: "due", FlowID: "f1", Environment: "sandbox", Version: 5, At: now.Add(-time.Minute), Status: StatusPending})
	seedSchedule(t, st, View{ScheduleID: "future", FlowID: "f1", Environment: "sandbox", Version: 6, At: now.Add(time.Hour), Status: StatusPending})

	cmd := &fakeCmd{}
	s := &Scheduler{store: st, cmd: cmd, now: func() time.Time { return now }}
	sum, err := s.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Activated != 1 || len(cmd.activated) != 1 || cmd.activated[0] != "due" {
		t.Fatalf("expected only the due schedule activated: %+v", cmd.activated)
	}
	if cmd.priors["due"] != 3 {
		t.Fatalf("prior version should be the live v3, got %d", cmd.priors["due"])
	}
}

func TestSchedulerRevertsExpiredTimeBox(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)

	// An active time-boxed schedule whose window has elapsed → revert.
	seedSchedule(t, st, View{ScheduleID: "expired", FlowID: "f1", Environment: "sandbox", Version: 5, Status: StatusActive, Until: &past, PriorVersion: 3})
	// An active time-boxed schedule still within its window → leave alone.
	seedSchedule(t, st, View{ScheduleID: "open", FlowID: "f1", Environment: "sandbox", Version: 5, Status: StatusActive, Until: &future, PriorVersion: 3})
	// An active schedule with no window (not time-boxed) → never reverts.
	seedSchedule(t, st, View{ScheduleID: "permanent", FlowID: "f1", Environment: "sandbox", Version: 5, Status: StatusActive})

	cmd := &fakeCmd{}
	s := &Scheduler{store: st, cmd: cmd, now: func() time.Time { return now }}
	sum, err := s.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Reverted != 1 || len(cmd.reverted) != 1 || cmd.reverted[0] != "expired" {
		t.Fatalf("expected only the expired time-box reverted: %+v", cmd.reverted)
	}
}
