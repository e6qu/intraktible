// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestFlowVersioningReplay(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()

	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "author"}
	h := command.NewHandler(log)
	flowID, _, err := h.CreateFlow(ctx, id, domain.CreateFlow{Slug: "onboarding", Name: "Onboarding Logic"})
	if err != nil {
		t.Fatal(err)
	}
	v1, _, _, err := h.PublishVersion(ctx, id, domain.PublishVersion{FlowID: flowID, Graph: flowtest.LinearGraph()})
	if err != nil {
		t.Fatal(err)
	}
	v2, _, _, err := h.PublishVersion(ctx, id, domain.PublishVersion{FlowID: flowID, Graph: flowtest.LinearGraph()})
	if err != nil {
		t.Fatal(err)
	}
	if v1 != 1 || v2 != 2 {
		t.Fatalf("version numbering: got %d,%d want 1,2", v1, v2)
	}

	// Rebuild the read model purely from the log.
	st := store.NewMemory()
	rt := projection.New(log, st, flows.Projector{})
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	fv, ok, err := flows.Read(ctx, st, id, flowID)
	if err != nil || !ok {
		t.Fatalf("read after replay: ok=%v err=%v", ok, err)
	}
	if fv.Slug != "onboarding" || fv.Latest != 2 || len(fv.Versions) != 2 {
		t.Fatalf("after replay: slug=%q latest=%d versions=%d, want onboarding/2/2",
			fv.Slug, fv.Latest, len(fv.Versions))
	}

	// Live path: a third publish must reach the projection via the bus.
	if _, _, _, err := h.PublishVersion(ctx, id, domain.PublishVersion{FlowID: flowID, Graph: flowtest.LinearGraph()}); err != nil {
		t.Fatal(err)
	}
	if !testutil.Eventually(t, func() bool {
		got, ok, _ := flows.Read(ctx, st, id, flowID)
		return ok && got.Latest == 3
	}) {
		t.Fatal("live projection did not reach version 3")
	}
}

func TestSlugUniquenessAndTenantIsolation(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	h := command.NewHandler(log)
	a := identity.Identity{Org: "a", Workspace: "main", Actor: "x"}
	b := identity.Identity{Org: "b", Workspace: "main", Actor: "y"}

	if _, _, err := h.CreateFlow(ctx, a, domain.CreateFlow{Slug: "dup", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.CreateFlow(ctx, a, domain.CreateFlow{Slug: "dup", Name: "A2"}); err == nil {
		t.Fatal("expected slug-uniqueness error within tenant a")
	}
	// Same slug in another tenant is allowed.
	if _, _, err := h.CreateFlow(ctx, b, domain.CreateFlow{Slug: "dup", Name: "B"}); err != nil {
		t.Fatalf("same slug in tenant b should be allowed: %v", err)
	}

	st := store.NewMemory()
	if err := projection.New(log, st, flows.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	la, _ := flows.List(ctx, st, a)
	lb, _ := flows.List(ctx, st, b)
	if len(la) != 1 || len(lb) != 1 {
		t.Fatalf("tenant isolation: a=%d b=%d, want 1/1", len(la), len(lb))
	}
}

// TestFlowDescriptionLifecycle covers the description end-to-end at this layer:
// created with one, updated via UpdateFlow (PATCH semantics: name untouched),
// and — schema evolution — a legacy FlowCreated event recorded before the field
// existed replays with an empty description.
func TestFlowDescriptionLifecycle(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()

	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "author"}
	h := command.NewHandler(log)
	flowID, _, err := h.CreateFlow(ctx, id, domain.CreateFlow{
		Slug: "described", Name: "Described", Description: "Scores loan applications into approve/refer bands.",
	})
	if err != nil {
		t.Fatal(err)
	}

	// A legacy flow.created payload without the description field (recorded before
	// the field existed) must decode and replay cleanly.
	legacy, err := json.Marshal(map[string]string{"flow_id": "legacy-1", "slug": "legacy", "name": "Legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: events.StreamFlows, Type: events.TypeFlowCreated, Time: time.Now().UTC(), Payload: legacy,
	}); err != nil {
		t.Fatal(err)
	}

	// PATCH semantics: setting only the description leaves the name untouched.
	desc := "Updated: adds the Reg B adverse-action codes."
	name, gotDesc, _, err := h.UpdateFlow(ctx, id, domain.UpdateFlow{FlowID: flowID, Description: &desc})
	if err != nil {
		t.Fatal(err)
	}
	if name != "Described" || gotDesc != desc {
		t.Fatalf("update resolved to %q/%q, want Described/%q", name, gotDesc, desc)
	}
	if _, _, _, err := h.UpdateFlow(ctx, id, domain.UpdateFlow{FlowID: "nope", Description: &desc}); err == nil {
		t.Fatal("expected error updating unknown flow")
	}

	st := store.NewMemory()
	if err := projection.New(log, st, flows.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	fv, ok, err := flows.Read(ctx, st, id, flowID)
	if err != nil || !ok {
		t.Fatalf("read after replay: ok=%v err=%v", ok, err)
	}
	if fv.Name != "Described" || fv.Description != desc {
		t.Fatalf("after replay: name=%q description=%q, want Described/%q", fv.Name, fv.Description, desc)
	}
	lv, ok, err := flows.Read(ctx, st, id, "legacy-1")
	if err != nil || !ok {
		t.Fatalf("legacy read after replay: ok=%v err=%v", ok, err)
	}
	if lv.Description != "" {
		t.Fatalf("legacy event should replay with an empty description, got %q", lv.Description)
	}
}

func TestPublishUnknownFlowFails(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	h := command.NewHandler(log)
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "a"}
	if _, _, _, err := h.PublishVersion(ctx, id, domain.PublishVersion{FlowID: "nope", Graph: flowtest.LinearGraph()}); err == nil {
		t.Fatal("expected error publishing to unknown flow")
	}
}

// Pure graph/etag/command validation lives in decision-engine/domain unit tests
// (decision-engine/domain/domain_test.go); this file stays at the integration
// layer of the pyramid (command -> event -> projection -> replay).
