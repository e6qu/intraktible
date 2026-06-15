// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func TestVersionPinningAndABRouting(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "author"}

	h := command.NewHandler(log)
	flowID, _, err := h.CreateFlow(ctx, id, domain.CreateFlow{Slug: "router", Name: "Router"})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"v1", "v2"} {
		if _, _, _, err := h.PublishVersion(ctx, id, domain.PublishVersion{FlowID: flowID, Graph: flowtest.ConstGraph(v)}); err != nil {
			t.Fatal(err)
		}
	}

	// readModel rebuilds the flow registry from the log (synchronously), so it
	// reflects every deploy made so far without bus lag.
	readModel := func() store.Store {
		s := store.NewMemory()
		if err := projection.New(log, s, flows.Projector{}).Start(ctx); err != nil {
			t.Fatal(err)
		}
		return s
	}
	decide := func(s store.Store, roll int) string {
		dh := command.NewDecideHandler(log, s, command.WithRoll(func() int { return roll }))
		res, err := dh.Decide(ctx, id, "router", "production", nil, command.EntityRef{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != domain.StatusCompleted {
			t.Fatalf("status=%s err=%s", res.Status, res.Error)
		}
		return res.Output["decision"].(string)
	}
	deploy := func(c domain.DeployVersion) {
		c.FlowID, c.Environment = flowID, "production"
		if _, err := h.Deploy(ctx, id, c); err != nil {
			t.Fatal(err)
		}
	}

	// No deployment -> falls back to the latest published version (v2).
	if got := decide(readModel(), 50); got != "v2" {
		t.Fatalf("no deployment: got %q, want v2 (latest)", got)
	}

	// Pin production to v1 even though v2 is the latest.
	deploy(domain.DeployVersion{Version: 1})
	if got := decide(readModel(), 99); got != "v1" {
		t.Fatalf("pinned: got %q, want v1", got)
	}

	// A/B with 100% challenger -> always the challenger (v2).
	deploy(domain.DeployVersion{Version: 1, ChallengerVersion: 2, ChallengerPct: 100})
	s := readModel()
	if got := decide(s, 0); got != "v2" {
		t.Fatalf("100%% challenger: got %q, want v2", got)
	}

	// A/B split at 50%: the draw decides champion vs challenger.
	deploy(domain.DeployVersion{Version: 1, ChallengerVersion: 2, ChallengerPct: 50})
	s = readModel()
	if got := decide(s, 10); got != "v2" { // 10 < 50 -> challenger
		t.Fatalf("split (roll 10): got %q, want v2", got)
	}
	if got := decide(s, 80); got != "v1" { // 80 >= 50 -> champion
		t.Fatalf("split (roll 80): got %q, want v1", got)
	}

	// The chosen variant is recorded in history (replay-stable A/B).
	hist := store.NewMemory()
	if err := projection.New(log, hist, history.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	recs, err := history.List(ctx, hist, id)
	if err != nil {
		t.Fatal(err)
	}
	var sawChampion, sawChallenger bool
	for _, r := range recs {
		switch r.Variant {
		case "champion":
			sawChampion = true
		case "challenger":
			sawChallenger = true
		}
	}
	if !sawChampion || !sawChallenger {
		t.Fatalf("history should record both variants: champion=%v challenger=%v", sawChampion, sawChallenger)
	}
}

func TestDeployValidationAndUnknownVersion(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "author"}
	h := command.NewHandler(log)
	flowID, _, err := h.CreateFlow(ctx, id, domain.CreateFlow{Slug: "r", Name: "R"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.PublishVersion(ctx, id, domain.PublishVersion{FlowID: flowID, Graph: flowtest.ConstGraph("v1")}); err != nil {
		t.Fatal(err)
	}
	// Deploying an unpublished version fails loudly.
	if _, err := h.Deploy(ctx, id, domain.DeployVersion{FlowID: flowID, Environment: "production", Version: 5}); err == nil {
		t.Fatal("expected error deploying an unpublished version")
	}
	// Bad environment is rejected by command validation.
	if _, err := h.Deploy(ctx, id, domain.DeployVersion{FlowID: flowID, Environment: "staging", Version: 1}); err == nil {
		t.Fatal("expected error for invalid environment")
	}
}
