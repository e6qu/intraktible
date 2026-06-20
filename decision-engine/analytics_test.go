// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/analytics"
	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestAnalyticsMetrics(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "author"}

	h := command.NewHandler(log)
	flowID, _, err := h.CreateFlow(ctx, id, domain.CreateFlow{Slug: "metrics", Name: "Metrics"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.PublishVersion(ctx, id, domain.PublishVersion{FlowID: flowID, Graph: flowtest.ConstGraph("ok")}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.PublishVersion(ctx, id, domain.PublishVersion{FlowID: flowID, Graph: flowtest.FailingGraph()}); err != nil {
		t.Fatal(err)
	}

	// One live read model feeds the decide path; deployments reach it via the bus.
	rm := store.NewMemory()
	if err := projection.New(log, rm, flows.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	dh := command.NewDecideHandler(log, rm)
	approver := identity.Identity{Org: "demo", Workspace: "main", Actor: "approver"}
	deployAndWait := func(dep domain.DeployVersion) {
		dep.FlowID, dep.Environment = flowID, "production"
		reqID, _, err := h.RequestDeployment(ctx, id, dep)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.ApproveDeployment(ctx, approver, flowID, reqID, ""); err != nil {
			t.Fatal(err)
		}
		if !testutil.Eventually(t, func() bool {
			fv, _, _ := flows.Read(ctx, rm, id, flowID)
			d, ok := fv.Deployments["production"]
			return ok && d.Version == dep.Version && d.ChallengerVersion == dep.ChallengerVersion && d.ChallengerPct == dep.ChallengerPct
		}) {
			t.Fatal("deployment did not reach the read model")
		}
	}
	decide := func(wantStatus string) {
		res, err := dh.Decide(ctx, id, "metrics", "production", nil, command.EntityRef{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != wantStatus {
			t.Fatalf("decide status=%s, want %s (err=%s)", res.Status, wantStatus, res.Error)
		}
	}

	// 2 champion decisions on v1 (completed).
	deployAndWait(domain.DeployVersion{Version: 1})
	decide(string(domain.StatusCompleted))
	decide(string(domain.StatusCompleted))
	// 1 challenger decision on v2 (fails loudly).
	deployAndWait(domain.DeployVersion{Version: 1, ChallengerVersion: 2, ChallengerPct: 100})
	decide(string(domain.StatusFailed))

	// Rebuild the metrics read model purely from the decision event stream.
	ms := store.NewMemory()
	if err := projection.New(log, ms, analytics.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	m, ok, err := analytics.Read(ctx, ms, id, flowID)
	if err != nil || !ok {
		t.Fatalf("metrics read: ok=%v err=%v", ok, err)
	}
	if m.Total != 3 || m.Completed != 2 || m.Failed != 1 {
		t.Fatalf("totals: %+v", m)
	}
	if m.ByEnvironment["production"] != 3 {
		t.Fatalf("by_environment: %v", m.ByEnvironment)
	}
	if m.ByVersion[1] != 2 || m.ByVersion[2] != 1 {
		t.Fatalf("by_version: %v", m.ByVersion)
	}
	champ, chall := m.ByVariant["champion"], m.ByVariant["challenger"]
	if champ.Started != 2 || champ.Completed != 2 || champ.Failed != 0 {
		t.Fatalf("champion variant: %+v", champ)
	}
	if chall.Started != 1 || chall.Failed != 1 || chall.Completed != 0 {
		t.Fatalf("challenger variant: %+v", chall)
	}
}
