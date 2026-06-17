// SPDX-License-Identifier: AGPL-3.0-or-later

package monitor_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/analytics"
	"github.com/e6qu/intraktible/decision-engine/monitor"
	"github.com/e6qu/intraktible/decision-engine/notify"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestSchedulerNotifiesOnFiringEdge(t *testing.T) {
	ctx := context.Background()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "author"}
	log, st := testutil.NewLogStore(t)

	var hits atomic.Int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	monCmd := monitor.NewHandler(log)
	notifier := notify.NewNotifier(log, st, http.DefaultClient)
	hooks := notify.New(notify.NewHandler(log), st)
	mon := monitor.New(monCmd, st, notifier)
	api := testutil.StartAPI(t, log, st, "test-key", id, func(mux *http.ServeMux) {
		hooks.Routes(mux)
		mon.Routes(mux)
	}, monitor.Projector{}, notify.Projector{}, analytics.Projector{})

	api.Request(t, http.MethodPost, "/v1/webhooks", map[string]any{"url": hook.URL}, http.StatusCreated, nil)
	api.Request(t, http.MethodPost, "/v1/flows/f1/monitors", map[string]any{"metric": "volume", "op": "gt", "threshold": 0}, http.StatusCreated, nil)

	// Seed metrics so volume (5) breaches the threshold. The analytics projector
	// only writes on decision events, so this seeded snapshot is left intact.
	if err := store.PutDoc(ctx, st, analytics.Collection, store.Key(id.Org, id.Workspace, "f1"),
		analytics.FlowMetrics{Org: id.Org, Workspace: id.Workspace, FlowID: "f1", Total: 5, Completed: 5,
			ByEnvironment: map[string]int{}, ByVersion: map[int]int{}, ByDisposition: map[string]int{}}); err != nil {
		t.Fatal(err)
	}

	// Wait until the webhook + monitor projections are live before sweeping.
	type mon1 struct {
		Monitors []struct {
			Alerting bool `json:"alerting"`
		} `json:"monitors"`
	}
	var got mon1
	if !testutil.Eventually(t, func() bool {
		got = mon1{}
		api.Request(t, http.MethodGet, "/v1/flows/f1/monitors", nil, http.StatusOK, &got)
		return len(got.Monitors) == 1
	}) {
		t.Fatal("monitor projection never appeared")
	}

	sched := monitor.NewScheduler(st, monCmd, notifier)

	// First sweep: the monitor crosses ok→firing, so it alerts and delivers once.
	sum, err := sched.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if sum.Alerted != 1 || sum.Delivered != 1 {
		t.Fatalf("first sweep should alert+deliver once: %+v", sum)
	}
	if hits.Load() != 1 {
		t.Fatalf("webhook should have been hit once, got %d", hits.Load())
	}

	// The alert state is recorded; wait for the projection to flip Alerting.
	if !testutil.Eventually(t, func() bool {
		got = mon1{}
		api.Request(t, http.MethodGet, "/v1/flows/f1/monitors", nil, http.StatusOK, &got)
		return len(got.Monitors) == 1 && got.Monitors[0].Alerting
	}) {
		t.Fatal("monitor never marked alerting")
	}

	// Second sweep: still firing, already alerted — no re-notify (dedup).
	sum2, err := sched.Tick(ctx)
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if sum2.Alerted != 0 || sum2.Delivered != 0 {
		t.Fatalf("second sweep must not re-alert: %+v", sum2)
	}
	if hits.Load() != 1 {
		t.Fatalf("webhook must not be hit again, got %d", hits.Load())
	}
}
