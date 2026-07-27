// SPDX-License-Identifier: AGPL-3.0-or-later

package notify_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/notify"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func webhookEvent(t *testing.T, seq uint64, typ string, payload any) eventlog.Envelope {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return eventlog.Envelope{
		Org: "demo", Workspace: "main", Actor: "ops", Seq: seq, Type: typ,
		Time: time.Unix(int64(seq), 0).UTC(), Payload: raw,
	}
}

func TestUnsubscribeRejectsUnknownAndAlreadyInactiveWithoutAppending(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	h := notify.NewHandler(log)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "ops"}

	if _, err := h.Unsubscribe(ctx, id, "missing"); err == nil {
		t.Fatal("unsubscribe of unknown webhook succeeded")
	}
	if log.Head() != 0 {
		t.Fatalf("unknown unsubscribe appended an event: head=%d", log.Head())
	}
	webhookID, _, err := h.Subscribe(ctx, id, "https://example.com/hook", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Unsubscribe(ctx, id, webhookID); err != nil {
		t.Fatal(err)
	}
	head := log.Head()
	if _, err := h.Unsubscribe(ctx, id, webhookID); err == nil {
		t.Fatal("second unsubscribe succeeded")
	}
	if log.Head() != head {
		t.Fatalf("second unsubscribe appended an event: head=%d want %d", log.Head(), head)
	}
}

func TestProjectorRetainsUnsubscribedWebhookForInFlightDelivery(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	projector := notify.Projector{}
	if err := projector.Apply(ctx, webhookEvent(t, 1, notify.TypeSubscribed, notify.Subscribed{
		WebhookID: "hook-1", URL: "https://example.com/hook",
	}), st); err != nil {
		t.Fatal(err)
	}
	if err := projector.Apply(ctx, webhookEvent(t, 2, notify.TypeUnsubscribed, notify.Unsubscribed{
		WebhookID: "hook-1",
	}), st); err != nil {
		t.Fatal(err)
	}
	if err := projector.Apply(ctx, webhookEvent(t, 3, notify.TypeDelivered, notify.Delivered{
		WebhookID: "hook-1", URL: "https://example.com/hook", OK: true, Status: 204,
		At: time.Unix(3, 0).UTC(),
	}), st); err != nil {
		t.Fatalf("in-flight delivery after unsubscribe: %v", err)
	}

	id := identity.Identity{Org: "demo", Workspace: "main"}
	list, err := notify.List(ctx, st, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("inactive webhook leaked into active list: %+v", list)
	}
	view, ok, err := store.GetDoc[notify.View](
		ctx, st, notify.Collection, store.Key(id.Org, id.Workspace, "hook-1"),
	)
	if err != nil || !ok {
		t.Fatalf("read retained webhook: ok=%v err=%v", ok, err)
	}
	if view.Active || view.DeliveryCount != 1 || view.LastStatus != 204 {
		t.Fatalf("retained webhook state = %+v", view)
	}
}

func TestProjectorRejectsUnknownWebhookTransitions(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	projector := notify.Projector{}

	err := projector.Apply(ctx, webhookEvent(t, 1, notify.TypeUnsubscribed, notify.Unsubscribed{
		WebhookID: "missing",
	}), st)
	if err == nil || !strings.Contains(err.Error(), "unknown webhook") {
		t.Fatalf("unknown unsubscribe error = %v", err)
	}
	err = projector.Apply(ctx, webhookEvent(t, 2, notify.TypeDelivered, notify.Delivered{
		WebhookID: "missing", At: time.Unix(2, 0).UTC(),
	}), st)
	if err == nil || !strings.Contains(err.Error(), "unknown webhook") {
		t.Fatalf("unknown delivery error = %v", err)
	}
}
