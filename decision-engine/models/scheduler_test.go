// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"context"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/notify"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// fakeAlertCmd records the alert/resolve commands and mutates the stored stats to
// flip Alerting — standing in for the DriftProjector folding the recorded events,
// so the scheduler's dedup is exercised across ticks.
type fakeAlertCmd struct {
	store    store.Store
	alerted  int
	resolved int
}

func (f *fakeAlertCmd) MarkModelDriftAlerted(ctx context.Context, id identity.Identity, name string, _, _ float64) (eventlog.Envelope, error) {
	f.alerted++
	_, err := store.UpdateDoc(ctx, f.store, StatsCollection, store.Key(id.Org, id.Workspace, name), func(st *ModelStats) { st.Alerting = true })
	return eventlog.Envelope{}, err
}

func (f *fakeAlertCmd) MarkModelDriftResolved(ctx context.Context, id identity.Identity, name string) (eventlog.Envelope, error) {
	f.resolved++
	_, err := store.UpdateDoc(ctx, f.store, StatsCollection, store.Key(id.Org, id.Workspace, name), func(st *ModelStats) { st.Alerting = false })
	return eventlog.Envelope{}, err
}

type fakeNotifier struct{ delivered int }

func (f *fakeNotifier) Deliver(_ context.Context, _ identity.Identity, _ string, _ any) ([]notify.DeliveryResult, error) {
	f.delivered++
	return nil, nil
}

// failingNotifier simulates every webhook being down (Deliver errors on total failure).
type failingNotifier struct{ calls int }

func (f *failingNotifier) Deliver(_ context.Context, _ identity.Identity, _ string, _ any) ([]notify.DeliveryResult, error) {
	f.calls++
	return nil, errDeliveryDown
}

var errDeliveryDown = fmtError("delivery down")

type fmtError string

func (e fmtError) Error() string { return string(e) }

// TestDriftSchedulerDeliveryFailureDoesNotStarveSweep proves a failing webhook on
// one model does not abort the sweep (so later models are still processed) and the
// failed model is NOT marked alerted (it retries next tick).
func TestDriftSchedulerDeliveryFailureDoesNotStarveSweep(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	base := Histogram{10, 10, 10, 10, 10, 10, 10, 10, 10, 10}
	drifted := Histogram{0, 0, 0, 0, 0, 0, 0, 0, 0, 100}
	for _, name := range []string{"a-model", "z-model"} { // two firing models
		seedStats(t, s, ModelStats{Org: "demo", Workspace: "main", Name: name,
			Hist: drifted, HasBaseline: true, BaselineHist: base, Threshold: 0.25})
	}
	cmd := &fakeAlertCmd{store: s}
	sched := NewScheduler(s, cmd, &failingNotifier{}, 0)

	sum, err := sched.Tick(ctx)
	if err != nil {
		t.Fatalf("tick should not error out on a delivery failure: %v", err)
	}
	if sum.Models != 2 {
		t.Fatalf("both models must be visited despite a delivery failure: %+v", sum)
	}
	if sum.DeliveryFailures != 2 || sum.Alerted != 0 || cmd.alerted != 0 {
		t.Fatalf("a failed delivery must not mark alerted (retry next tick): %+v alerted=%d", sum, cmd.alerted)
	}
}

func seedStats(t *testing.T, s store.Store, st ModelStats) {
	t.Helper()
	if err := store.PutDoc(context.Background(), s, StatsCollection, store.Key(st.Org, st.Workspace, st.Name), st); err != nil {
		t.Fatal(err)
	}
}

func TestDriftSchedulerFiringEdge(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	base := Histogram{10, 10, 10, 10, 10, 10, 10, 10, 10, 10}
	drifted := Histogram{0, 0, 0, 0, 0, 0, 0, 0, 0, 100} // PSI well past 0.25
	seedStats(t, s, ModelStats{
		Org: "demo", Workspace: "main", Name: "risk",
		Hist: drifted, HasBaseline: true, BaselineHist: base, Threshold: 0.25,
	})

	cmd := &fakeAlertCmd{store: s}
	not := &fakeNotifier{}
	sched := NewScheduler(s, cmd, not, 0)

	// First sweep: crosses ok→firing, so it alerts and delivers exactly once.
	sum, err := sched.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if sum.Alerted != 1 || sum.Delivered != 1 || not.delivered != 1 {
		t.Fatalf("first sweep should alert+deliver once: %+v delivered=%d", sum, not.delivered)
	}

	// Second sweep: still firing, already alerting — dedup, no re-notify.
	sum2, err := sched.Tick(ctx)
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if sum2.Alerted != 0 || sum2.Delivered != 0 || not.delivered != 1 {
		t.Fatalf("second sweep must not re-alert: %+v delivered=%d", sum2, not.delivered)
	}

	// Drift subsides (distribution returns to baseline): firing→ok resolves.
	if _, err := store.UpdateDoc(ctx, s, StatsCollection, store.Key("demo", "main", "risk"),
		func(st *ModelStats) { st.Hist = base }); err != nil {
		t.Fatal(err)
	}
	sum3, err := sched.Tick(ctx)
	if err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if sum3.Resolved != 1 || cmd.resolved != 1 {
		t.Fatalf("third sweep should resolve once: %+v resolved=%d", sum3, cmd.resolved)
	}
}

func TestDriftSchedulerSkipsUnmonitored(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	base := Histogram{10, 10, 10, 10, 10, 10, 10, 10, 10, 10}
	drifted := Histogram{0, 0, 0, 0, 0, 0, 0, 0, 0, 100}
	// No threshold → can never fire.
	seedStats(t, s, ModelStats{Org: "demo", Workspace: "main", Name: "no-threshold",
		Hist: drifted, HasBaseline: true, BaselineHist: base})
	// Threshold but no baseline → drift undefined.
	seedStats(t, s, ModelStats{Org: "demo", Workspace: "main", Name: "no-baseline",
		Hist: drifted, Threshold: 0.25})

	cmd := &fakeAlertCmd{store: s}
	sched := NewScheduler(s, cmd, &fakeNotifier{}, 0)
	sum, err := sched.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if sum.Models != 0 || sum.Alerted != 0 {
		t.Fatalf("unmonitored models must be skipped: %+v", sum)
	}
}

// A nil notifier still maintains the alert/resolve state (delivery just no-ops),
// so an operator who hasn't wired a webhook yet still gets the dedup bookkeeping.
func TestDriftSchedulerNilNotifier(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	base := Histogram{10, 10, 10, 10, 10, 10, 10, 10, 10, 10}
	seedStats(t, s, ModelStats{Org: "demo", Workspace: "main", Name: "risk",
		Hist: Histogram{0, 0, 0, 0, 0, 0, 0, 0, 0, 100}, HasBaseline: true, BaselineHist: base, Threshold: 0.25})

	cmd := &fakeAlertCmd{store: s}
	sched := NewScheduler(s, cmd, nil, 0)
	sum, err := sched.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if sum.Alerted != 1 || sum.Delivered != 0 {
		t.Fatalf("nil notifier should alert without delivering: %+v", sum)
	}
}
