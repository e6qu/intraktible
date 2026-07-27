// SPDX-License-Identifier: AGPL-3.0-or-later

package flows

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

func flowEvent(t *testing.T, seq uint64, typ, actor string, payload any) eventlog.Envelope {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return eventlog.Envelope{
		Org: "org", Workspace: "workspace", Stream: "decision.flow:flow-1",
		Type: typ, Actor: actor, Seq: seq, Time: time.Unix(int64(seq), 0).UTC(), Payload: raw,
	}
}

func seedFlow(t *testing.T, ctx context.Context, st store.Store) {
	t.Helper()
	err := (Projector{}).Apply(ctx, flowEvent(t, 1, events.TypeFlowCreated, "author", events.FlowCreated{
		FlowID: "flow-1", Slug: "credit", Name: "Credit",
	}), st)
	if err != nil {
		t.Fatal(err)
	}
}

func TestTerminalDeploymentEventsRequirePendingRequestBeforeMutation(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		typ     string
		payload any
	}{
		{
			name: "approval",
			typ:  events.TypeDeploymentApproved,
			payload: events.DeploymentApproved{
				RequestID: "missing", FlowID: "flow-1", Environment: "production", Version: 7,
			},
		},
		{
			name:    "rejection",
			typ:     events.TypeDeploymentRejected,
			payload: events.DeploymentRejected{RequestID: "missing", FlowID: "flow-1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := store.NewMemory()
			seedFlow(t, ctx, st)

			err := (Projector{}).Apply(ctx, flowEvent(t, 2, tc.typ, "checker", tc.payload), st)
			if err == nil || !strings.Contains(err.Error(), `unknown request "missing"`) {
				t.Fatalf("terminal event error = %v, want unknown request", err)
			}
			got, ok, readErr := Read(ctx, st, identity.Identity{Org: "org", Workspace: "workspace"}, "flow-1")
			if readErr != nil || !ok {
				t.Fatalf("read flow: ok=%v err=%v", ok, readErr)
			}
			if len(got.Deployments) != 0 {
				t.Fatalf("terminal event mutated deployments before validation: %+v", got.Deployments)
			}
			if !got.UpdatedAt.Equal(time.Unix(1, 0).UTC()) {
				t.Fatalf("terminal event changed updated_at: %v", got.UpdatedAt)
			}
		})
	}
}

func TestDeploymentRequestCanTransitionOnlyOnce(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	seedFlow(t, ctx, st)

	requested := events.DeploymentRequested{
		RequestID: "request-1", FlowID: "flow-1", Environment: "production", Version: 1,
	}
	if err := (Projector{}).Apply(
		ctx, flowEvent(t, 2, events.TypeDeploymentRequested, "maker", requested), st,
	); err != nil {
		t.Fatal(err)
	}
	approved := events.DeploymentApproved{
		RequestID: "request-1", FlowID: "flow-1", Environment: "production", Version: 1,
	}
	if err := (Projector{}).Apply(
		ctx, flowEvent(t, 3, events.TypeDeploymentApproved, "checker", approved), st,
	); err != nil {
		t.Fatal(err)
	}
	err := (Projector{}).Apply(
		ctx, flowEvent(t, 4, events.TypeDeploymentRejected, "other-checker", events.DeploymentRejected{
			RequestID: "request-1", FlowID: "flow-1", Reason: "too late",
		}), st,
	)
	if err == nil || !strings.Contains(err.Error(), "already approved") {
		t.Fatalf("second terminal event error = %v, want already approved", err)
	}

	got, ok, readErr := Read(ctx, st, identity.Identity{Org: "org", Workspace: "workspace"}, "flow-1")
	if readErr != nil || !ok {
		t.Fatalf("read flow: ok=%v err=%v", ok, readErr)
	}
	if got.DeploymentRequests[0].Status != RequestApproved ||
		got.Deployments["production"].Version != 1 ||
		!got.UpdatedAt.Equal(time.Unix(3, 0).UTC()) {
		t.Fatalf("second terminal event changed approved state: %+v", got)
	}
}

func TestBySlugUsesOwnedIndexAndRejectsDanglingEntry(t *testing.T) {
	ctx := context.Background()
	id := identity.Identity{Org: "org", Workspace: "workspace"}

	t.Run("missing index is not found", func(t *testing.T) {
		st := store.NewMemory()
		if err := store.PutDoc(ctx, st, Collection, store.Key(id.Org, id.Workspace, "flow-1"), FlowView{
			Org: id.Org, Workspace: id.Workspace, FlowID: "flow-1", Slug: "credit",
		}); err != nil {
			t.Fatal(err)
		}
		_, found, err := BySlug(ctx, st, id, "credit")
		if err != nil || found {
			t.Fatalf("BySlug without owned index: found=%v err=%v", found, err)
		}
	})

	t.Run("dangling index fails loudly", func(t *testing.T) {
		st := store.NewMemory()
		if err := store.PutDoc(
			ctx, st, slugIndexCollection, store.Key(id.Org, id.Workspace, "credit"),
			slugRef{FlowID: "missing-flow"},
		); err != nil {
			t.Fatal(err)
		}
		_, found, err := BySlug(ctx, st, id, "credit")
		if err == nil || found || !strings.Contains(err.Error(), "points to missing flow") {
			t.Fatalf("BySlug dangling index: found=%v err=%v", found, err)
		}
	})
}
