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
	summary, err := n.Deliver(context.Background(), id, "monitor check", map[string]any{"hello": "world"})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(summary.Results) != 2 || summary.Accepted != 1 || summary.Retryable != 1 {
		t.Fatalf("want 2 results (1 accepted, 1 retryable), got %+v", summary)
	}
	byID := map[string]notify.DeliveryResult{}
	for _, r := range summary.Results {
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

// When every active webhook fails RETRYABLY (5xx), the summary is RetryWorthy with no
// error — so a scheduler does NOT record the firing-edge alert (which would dedup it
// into silence) and retries next tick. The error return is reserved for real failures.
func TestDeliverAllRetryableIsRetryWorthy(t *testing.T) {
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
	summary, err := n.Deliver(context.Background(), id, "monitor check", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("a retryable total failure is not a real error: %v", err)
	}
	if !summary.RetryWorthy() || summary.Delivered() || summary.Retryable != 1 {
		t.Fatalf("all-retryable should be RetryWorthy and not Delivered: %+v", summary)
	}

	// No active webhooks at all is a vacuous success: nothing to deliver, not retry-worthy.
	empty := identity.Identity{Org: "demo", Workspace: "empty", Actor: "tester"}
	es, err := n.Deliver(context.Background(), empty, "monitor check", map[string]any{"x": 1})
	if err != nil || es.RetryWorthy() || es.Delivered() {
		t.Fatalf("no webhooks should be a vacuous success: %+v err=%v", es, err)
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
	summary, err := n.Deliver(context.Background(), id, "monitor check", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("a permanent 4xx must not signal a retry, got err=%v", err)
	}
	if summary.RetryWorthy() {
		t.Fatalf("an all-permanent failure must not be retry-worthy (it dedups), got %+v", summary)
	}
	if len(summary.Results) != 1 || summary.Permanent != 1 || summary.Results[0].Outcome != notify.OutcomePermanent {
		t.Fatalf("expected one permanent failure, got %+v", summary)
	}
}

// DeliverySummary's predicates encode the retry/dedup/delivered decisions as typed
// state (replacing the old error-as-control-flow), so the schedulers can't misread it.
func TestDeliverySummaryPredicates(t *testing.T) {
	if (notify.DeliverySummary{}).RetryWorthy() {
		t.Fatal("an empty summary (no webhooks) is not retry-worthy")
	}
	if !(notify.DeliverySummary{Retryable: 1}).RetryWorthy() {
		t.Fatal("nothing accepted + a retryable failure is retry-worthy")
	}
	if (notify.DeliverySummary{Permanent: 2}).RetryWorthy() {
		t.Fatal("all-permanent is NOT retry-worthy (it dedups)")
	}
	if (notify.DeliverySummary{Accepted: 1, Retryable: 1}).RetryWorthy() {
		t.Fatal("a partial success is not retry-worthy")
	}
	if !(notify.DeliverySummary{Accepted: 1}).Delivered() || (notify.DeliverySummary{Permanent: 1}).Delivered() {
		t.Fatal("Delivered iff at least one endpoint accepted")
	}
}

// A per-webhook template formats the body, and an event filter routes deliveries.
func TestDeliverTemplateAndRouting(t *testing.T) {
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "tester"}
	log, st := testutil.NewLogStore(t)

	var templated, plain string
	tmplSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		templated = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer tmplSrv.Close()
	driftSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		plain = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer driftSrv.Close()

	put := func(v notify.View) {
		v.Org, v.Workspace, v.Active = id.Org, id.Workspace, true
		if err := store.PutDoc(context.Background(), st, notify.Collection, store.Key(id.Org, id.Workspace, v.WebhookID), v); err != nil {
			t.Fatal(err)
		}
	}
	// A templated webhook that only wants "monitor" events.
	put(notify.View{WebhookID: "slack", URL: tmplSrv.URL, Template: `alert for {{.flow_id}}`, Events: []string{"monitor"}})
	// A webhook that only wants "drift" events — must NOT receive the monitor alert.
	put(notify.View{WebhookID: "drift", URL: driftSrv.URL, Events: []string{"drift"}})

	n := notify.NewNotifier(log, st, http.DefaultClient)
	summary, err := n.Deliver(context.Background(), id, "monitor check", map[string]any{"flow_id": "kyc"})
	if err != nil {
		t.Fatal(err)
	}
	// Only the matching (slack) webhook was delivered to.
	if len(summary.Results) != 1 || summary.Results[0].WebhookID != "slack" {
		t.Fatalf("routing should deliver only to the monitor webhook: %+v", summary.Results)
	}
	if templated != "alert for kyc" {
		t.Fatalf("template not rendered: %q", templated)
	}
	if plain != "" {
		t.Fatalf("the drift-only webhook should not have received the monitor alert: %q", plain)
	}
}
