// SPDX-License-Identifier: AGPL-3.0-or-later

package notify_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/notify"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestDeliver(t *testing.T) {
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "tester"}
	log, st := testutil.NewLogStore(t)

	var got map[string]any
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()

	// Seed the read model directly (the projection is exercised in the e2e).
	put := func(wid, url string) {
		if err := store.PutDoc(context.Background(), st, notify.Collection,
			store.Key(id.Org, id.Workspace, wid),
			notify.View{Org: id.Org, Workspace: id.Workspace, WebhookID: wid, URL: url, Active: true}); err != nil {
			t.Fatal(err)
		}
	}
	put("w-ok", ok.URL)
	put("w-bad", bad.URL)

	n := notify.NewNotifier(log, st, http.DefaultClient)
	results, err := n.Deliver(context.Background(), id, "monitor check", map[string]any{"hello": "world"})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	byID := map[string]notify.DeliveryResult{}
	for _, r := range results {
		byID[r.WebhookID] = r
	}
	if !byID["w-ok"].OK || byID["w-ok"].Status != 200 || byID["w-ok"].Outcome != notify.OutcomeAccepted {
		t.Fatalf("ok webhook: %+v", byID["w-ok"])
	}
	if byID["w-bad"].OK || byID["w-bad"].Status != 500 || byID["w-bad"].Error == "" ||
		byID["w-bad"].Outcome != notify.OutcomeRetryable {
		t.Fatalf("bad webhook should report a retryable non-2xx failure: %+v", byID["w-bad"])
	}
	if got["hello"] != "world" {
		t.Fatalf("payload not delivered as JSON: %+v", got)
	}
}

// When every active webhook fails, Deliver returns an error — so a scheduler does
// NOT record the firing-edge alert (which would dedup it into silence) and retries.
func TestDeliverAllFailErrors(t *testing.T) {
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "tester"}
	log, st := testutil.NewLogStore(t)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if err := store.PutDoc(context.Background(), st, notify.Collection,
		store.Key(id.Org, id.Workspace, "w-bad"),
		notify.View{Org: id.Org, Workspace: id.Workspace, WebhookID: "w-bad", URL: bad.URL, Active: true}); err != nil {
		t.Fatal(err)
	}
	n := notify.NewNotifier(log, st, http.DefaultClient)
	if _, err := n.Deliver(context.Background(), id, "monitor check", map[string]any{"x": 1}); err == nil {
		t.Fatal("Deliver should error when all active webhooks fail")
	}

	// No active webhooks at all is a vacuous success (nothing to deliver).
	empty := identity.Identity{Org: "demo", Workspace: "empty", Actor: "tester"}
	if _, err := n.Deliver(context.Background(), empty, "monitor check", map[string]any{"x": 1}); err != nil {
		t.Fatalf("Deliver with no webhooks should succeed: %v", err)
	}
}

// A webhook that returns a permanent 4xx (e.g. 404/410 — gone for good) must NOT
// cause Deliver to signal a retry: returning an error there would keep the firing
// edge unrecorded and re-deliver to the dead endpoint on every single tick forever.
// The failure is still recorded (audit) but the alert is allowed to dedup.
func TestDeliverPermanentFailureDoesNotRetry(t *testing.T) {
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "tester"}
	log, st := testutil.NewLogStore(t)
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer gone.Close()
	if err := store.PutDoc(context.Background(), st, notify.Collection,
		store.Key(id.Org, id.Workspace, "w-gone"),
		notify.View{Org: id.Org, Workspace: id.Workspace, WebhookID: "w-gone", URL: gone.URL, Active: true}); err != nil {
		t.Fatal(err)
	}
	n := notify.NewNotifier(log, st, http.DefaultClient)
	results, err := n.Deliver(context.Background(), id, "monitor check", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("a permanent 4xx must not signal a retry, got err=%v", err)
	}
	if len(results) != 1 || results[0].Outcome != notify.OutcomePermanent || results[0].OK {
		t.Fatalf("expected one permanent failure, got %+v", results)
	}
}

func TestAnyAccepted(t *testing.T) {
	if notify.AnyAccepted(nil) {
		t.Fatal("no results must not count as accepted")
	}
	permanent := []notify.DeliveryResult{{Outcome: notify.OutcomePermanent}, {Outcome: notify.OutcomeRetryable}}
	if notify.AnyAccepted(permanent) {
		t.Fatal("an all-failure set must not count as accepted (no Delivered inflation)")
	}
	mixed := []notify.DeliveryResult{{Outcome: notify.OutcomePermanent}, {OK: true, Outcome: notify.OutcomeAccepted}}
	if !notify.AnyAccepted(mixed) {
		t.Fatal("a set with one accepted delivery must count as accepted")
	}
}
