// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/decision-engine/schedule"
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
	// Production deploys go through maker-checker: a request by the author, then an
	// approval by a *different* user (four-eyes).
	approver := identity.Identity{Org: "demo", Workspace: "main", Actor: "approver"}
	deploy := func(c domain.DeployVersion) {
		c.FlowID, c.Environment = flowID, "production"
		reqID, _, err := h.RequestDeployment(ctx, id, c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.ApproveDeployment(ctx, approver, flowID, reqID, ""); err != nil {
			t.Fatal(err)
		}
	}

	// No deployment -> production refuses to decide (no latest-published fallback
	// outside the sandbox; an un-deployed version must never take real traffic).
	{
		dh := command.NewDecideHandler(log, readModel())
		if _, err := dh.Decide(ctx, id, "router", "production", nil, command.EntityRef{}); err == nil {
			t.Fatal("expected decide without a production deployment to be refused")
		}
	}
	// The sandbox keeps the fallback: a freshly published flow is test-runnable there.
	{
		dh := command.NewDecideHandler(log, readModel())
		res, err := dh.Decide(ctx, id, "router", "sandbox", nil, command.EntityRef{})
		if err != nil || res.Output["decision"] != "v2" {
			t.Fatalf("sandbox fallback: got %v err=%v, want v2 (latest)", res.Output, err)
		}
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
	// Deploying an unpublished version fails loudly (sandbox: direct deploy allowed).
	if _, err := h.Deploy(ctx, id, domain.DeployVersion{FlowID: flowID, Environment: "sandbox", Version: 5}); err == nil {
		t.Fatal("expected error deploying an unpublished version")
	}
	// Bad environment is rejected by command validation.
	if _, err := h.Deploy(ctx, id, domain.DeployVersion{FlowID: flowID, Environment: "qa", Version: 1}); err == nil {
		t.Fatal("expected error for invalid environment")
	}
	// A direct deploy to production is refused — it must go through maker-checker.
	if _, err := h.Deploy(ctx, id, domain.DeployVersion{FlowID: flowID, Environment: "production", Version: 1}); err == nil {
		t.Fatal("expected a direct production deploy to be refused")
	}
}

func TestMakerCheckerApproval(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	maker := identity.Identity{Org: "demo", Workspace: "main", Actor: "maker"}
	checker := identity.Identity{Org: "demo", Workspace: "main", Actor: "checker"}

	h := command.NewHandler(log)
	flowID, _, err := h.CreateFlow(ctx, maker, domain.CreateFlow{Slug: "mc", Name: "MC"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.PublishVersion(ctx, maker, domain.PublishVersion{FlowID: flowID, Graph: flowtest.ConstGraph("v1")}); err != nil {
		t.Fatal(err)
	}

	// The maker proposes a production deployment.
	reqID, _, err := h.RequestDeployment(ctx, maker, domain.DeployVersion{FlowID: flowID, Environment: "production", Version: 1})
	if err != nil {
		t.Fatal(err)
	}

	// Four-eyes: the maker cannot approve their own request.
	if _, err := h.ApproveDeployment(ctx, maker, flowID, reqID, ""); err == nil {
		t.Fatal("the proposer must not be able to approve their own deployment (four-eyes)")
	}

	// A different user (the checker) approves it with an explanation, which deploys.
	if _, err := h.ApproveDeployment(ctx, checker, flowID, reqID, "backtest green, ship it"); err != nil {
		t.Fatal(err)
	}
	s := store.NewMemory()
	if err := projection.New(log, s, flows.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	fv, _, err := flows.Read(ctx, s, maker, flowID)
	if err != nil {
		t.Fatal(err)
	}
	if dep, ok := fv.Deployments["production"]; !ok || dep.Version != 1 {
		t.Fatalf("approval did not deploy: %+v", fv.Deployments)
	}
	if len(fv.DeploymentRequests) != 1 || fv.DeploymentRequests[0].Status != "approved" ||
		fv.DeploymentRequests[0].DecidedBy != "checker" {
		t.Fatalf("request not marked approved: %+v", fv.DeploymentRequests)
	}
	// The approver's explanation is recorded on the request (the audit trail).
	if fv.DeploymentRequests[0].Reason != "backtest green, ship it" {
		t.Fatalf("approval reason not recorded: %+v", fv.DeploymentRequests[0])
	}

	// Re-approving a decided request fails.
	if _, err := h.ApproveDeployment(ctx, checker, flowID, reqID, ""); err == nil {
		t.Fatal("re-approving an already-approved request should fail")
	}

	// A rejected request does not deploy.
	reqID2, _, err := h.RequestDeployment(ctx, maker, domain.DeployVersion{FlowID: flowID, Environment: "production", Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.RejectDeployment(ctx, checker, flowID, reqID2, "needs more testing"); err != nil {
		t.Fatal(err)
	}
}

// TestMakerCheckerStaleApproval covers a pending request whose environment moved
// underneath it. Two requests can be open at once, and approving the older one
// after the newer one shipped would silently revert live production traffic to the
// older version — with no signal that anything had changed since it was raised.
func TestMakerCheckerStaleApproval(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	maker := identity.Identity{Org: "demo", Workspace: "main", Actor: "maker"}
	checker := identity.Identity{Org: "demo", Workspace: "main", Actor: "checker"}

	h := command.NewHandler(log)
	flowID, _, err := h.CreateFlow(ctx, maker, domain.CreateFlow{Slug: "stale", Name: "Stale"})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"v1", "v2"} {
		if _, _, _, err := h.PublishVersion(ctx, maker, domain.PublishVersion{FlowID: flowID, Graph: flowtest.ConstGraph(v)}); err != nil {
			t.Fatal(err)
		}
	}

	// Two production requests are open at once: v1 first, then v2.
	req1, _, err := h.RequestDeployment(ctx, maker, domain.DeployVersion{FlowID: flowID, Environment: "production", Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	req2, _, err := h.RequestDeployment(ctx, maker, domain.DeployVersion{FlowID: flowID, Environment: "production", Version: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.ApproveDeployment(ctx, checker, flowID, req2, "ship v2"); err != nil {
		t.Fatal(err)
	}
	// req1 is still pending, and still pins v1. Approving it must fail rather than
	// roll production back.
	if _, err := h.ApproveDeployment(ctx, checker, flowID, req1, "lgtm"); err == nil {
		t.Fatal("approving a request whose environment was deployed onto since must fail")
	}

	s := store.NewMemory()
	if err := projection.New(log, s, flows.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	fv, _, err := flows.Read(ctx, s, maker, flowID)
	if err != nil {
		t.Fatal(err)
	}
	if dep := fv.Deployments["production"]; dep.Version != 2 {
		t.Fatalf("production must still serve v2, got v%d", dep.Version)
	}

	// A stale request can still be rejected — that is how it gets cleaned up.
	if _, err := h.RejectDeployment(ctx, checker, flowID, req1, "superseded by v2"); err != nil {
		t.Fatalf("rejecting a stale request should work: %v", err)
	}

	// A direct deploy to a non-production environment supersedes a request the same
	// way: the request no longer describes the change it would make.
	req3, _, err := h.RequestDeployment(ctx, maker, domain.DeployVersion{FlowID: flowID, Environment: "staging", Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Deploy(ctx, maker, domain.DeployVersion{FlowID: flowID, Environment: "staging", Version: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ApproveDeployment(ctx, checker, flowID, req3, ""); err == nil {
		t.Fatal("approving a request whose environment was directly deployed onto must fail")
	}
}

// TestMakerCheckerConcurrentDecision exercises the cross-process TOCTOU guard:
// two handlers on the SAME log (two nodes, each with its own in-process mutex)
// decide the same pending request. The per-request decision claim must let exactly
// one decision commit, even though neither mutex sees the other.
func TestMakerCheckerConcurrentDecision(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	maker := identity.Identity{Org: "demo", Workspace: "main", Actor: "maker"}
	approver := identity.Identity{Org: "demo", Workspace: "main", Actor: "approver"}
	rejecter := identity.Identity{Org: "demo", Workspace: "main", Actor: "rejecter"}

	// Two handlers share the log — two independent "nodes".
	nodeA := command.NewHandler(log)
	nodeB := command.NewHandler(log)

	flowID, _, err := nodeA.CreateFlow(ctx, maker, domain.CreateFlow{Slug: "race", Name: "Race"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := nodeA.PublishVersion(ctx, maker, domain.PublishVersion{FlowID: flowID, Graph: flowtest.ConstGraph("v1")}); err != nil {
		t.Fatal(err)
	}
	reqID, _, err := nodeA.RequestDeployment(ctx, maker, domain.DeployVersion{FlowID: flowID, Environment: "production", Version: 1})
	if err != nil {
		t.Fatal(err)
	}

	// Node A approves; node B concurrently rejects the same request. Both re-fold a
	// pending request (the simultaneity window), but they contend on the same
	// decision claim, so exactly one commits and the other conflicts/already-decided.
	type result struct{ err error }
	results := make(chan result, 2)
	start := make(chan struct{})
	go func() {
		<-start
		_, err := nodeA.ApproveDeployment(ctx, approver, flowID, reqID, "ship")
		results <- result{err}
	}()
	go func() {
		<-start
		_, err := nodeB.RejectDeployment(ctx, rejecter, flowID, reqID, "no")
		results <- result{err}
	}()
	close(start)
	r1, r2 := <-results, <-results

	committed := 0
	if r1.err == nil {
		committed++
	}
	if r2.err == nil {
		committed++
	}
	if committed != 1 {
		t.Fatalf("exactly one decision must commit, got %d (errs: %v, %v)", committed, r1.err, r2.err)
	}

	// The request ends in exactly one terminal state.
	s := store.NewMemory()
	if err := projection.New(log, s, flows.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	fv, _, err := flows.Read(ctx, s, maker, flowID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fv.DeploymentRequests) != 1 {
		t.Fatalf("want one request, got %+v", fv.DeploymentRequests)
	}
	if st := fv.DeploymentRequests[0].Status; st != "approved" && st != "rejected" {
		t.Fatalf("request not in a terminal state: %q", st)
	}
}

func TestScheduledProductionDeploymentIsGovernedAndAtomic(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	maker := identity.Identity{Org: "demo", Workspace: "main", Actor: "maker"}
	checker := identity.Identity{Org: "demo", Workspace: "main", Actor: "checker"}
	scheduler := identity.Identity{Org: "demo", Workspace: "main", Actor: "deploy-scheduler"}
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	at, until := now.Add(time.Hour), now.Add(2*time.Hour)
	h := command.NewHandler(log).WithNow(func() time.Time { return now })

	flowID, _, err := h.CreateFlow(ctx, maker, domain.CreateFlow{Slug: "scheduled-prod", Name: "Scheduled production"})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []string{"v1", "v2"} {
		if _, _, _, err := h.PublishVersion(ctx, maker, domain.PublishVersion{FlowID: flowID, Graph: flowtest.ConstGraph(result)}); err != nil {
			t.Fatal(err)
		}
	}
	// Establish v1 as the currently approved production version.
	baseReq, _, err := h.RequestDeployment(ctx, maker, domain.DeployVersion{FlowID: flowID, Environment: "production", Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.ApproveDeployment(ctx, checker, flowID, baseReq, "baseline"); err != nil {
		t.Fatal(err)
	}

	// The direct schedule path cannot evade production governance.
	if _, _, err := h.ScheduleDeploy(ctx, maker, flowID, "production", 2, at, &until); err == nil {
		t.Fatal("direct production scheduling must require maker-checker")
	}
	reqID, _, err := h.RequestScheduledDeployment(ctx, maker,
		domain.DeployVersion{FlowID: flowID, Environment: "production", Version: 2}, at, &until)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.ApproveDeployment(ctx, maker, flowID, reqID, "self approval"); err == nil {
		t.Fatal("scheduled production request allowed self-approval")
	}
	if _, err := h.ApproveDeployment(ctx, checker, flowID, reqID, "approved window"); err != nil {
		t.Fatal(err)
	}

	rebuild := func() store.Store {
		st := store.NewMemory()
		if err := projection.New(log, st, flows.Projector{}, schedule.Projector{}).Start(ctx); err != nil {
			t.Fatal(err)
		}
		return st
	}
	st := rebuild()
	fv, _, err := flows.Read(ctx, st, maker, flowID)
	if err != nil {
		t.Fatal(err)
	}
	if got := fv.Deployments["production"].Version; got != 1 {
		t.Fatalf("approval must schedule rather than deploy immediately: got v%d", got)
	}
	if len(fv.DeploymentRequests) != 2 || fv.DeploymentRequests[1].ScheduleID == "" ||
		fv.DeploymentRequests[1].At == nil || !fv.DeploymentRequests[1].At.Equal(at) {
		t.Fatalf("scheduled approval not visible on request: %+v", fv.DeploymentRequests)
	}
	schedules, err := schedule.List(ctx, st, maker, flowID)
	if err != nil || len(schedules) != 1 || schedules[0].Status != schedule.StatusPending {
		t.Fatalf("approved schedule not projected: %+v err=%v", schedules, err)
	}
	scheduleID := schedules[0].ScheduleID

	if err := h.ActivateSchedule(ctx, scheduler, scheduleID, flowID, "production", 2, 1); err != nil {
		t.Fatal(err)
	}
	st = rebuild()
	fv, _, _ = flows.Read(ctx, st, maker, flowID)
	schedules, _ = schedule.List(ctx, st, maker, flowID)
	if fv.Deployments["production"].Version != 2 || schedules[0].Status != schedule.StatusActive ||
		schedules[0].PriorVersion != 1 {
		t.Fatalf("activation was not atomic across projections: deployment=%+v schedule=%+v", fv.Deployments, schedules[0])
	}

	if err := h.RevertSchedule(ctx, scheduler, scheduleID, flowID, "production", 2, 1); err != nil {
		t.Fatal(err)
	}
	st = rebuild()
	fv, _, _ = flows.Read(ctx, st, maker, flowID)
	schedules, _ = schedule.List(ctx, st, maker, flowID)
	if fv.Deployments["production"].Version != 1 || schedules[0].Status != schedule.StatusReverted {
		t.Fatalf("revert was not atomic across projections: deployment=%+v schedule=%+v", fv.Deployments, schedules[0])
	}

	evs, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var activations, reverts, companionDeploys, companionRollbacks int
	for _, e := range evs {
		switch e.Type {
		case events.TypeDeployScheduleActivated:
			activations++
		case events.TypeDeployScheduleReverted:
			reverts++
		case events.TypeFlowVersionDeployed:
			companionDeploys++
		case events.TypeFlowVersionRolledBack:
			companionRollbacks++
		}
	}
	if activations != 1 || reverts != 1 || companionDeploys != 0 || companionRollbacks != 0 {
		t.Fatalf("schedule transitions must be one event each: activate=%d revert=%d deploy companions=%d rollback companions=%d",
			activations, reverts, companionDeploys, companionRollbacks)
	}
}

func TestCancelScheduledDeploymentIsStateAwareAndAtomic(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "editor"}
	scheduler := identity.Identity{Org: "demo", Workspace: "main", Actor: "deploy-scheduler"}
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	h := command.NewHandler(log).WithNow(func() time.Time { return now })

	flowID, _, err := h.CreateFlow(ctx, id, domain.CreateFlow{Slug: "cancel-window", Name: "Cancel window"})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []string{"v1", "v2", "v3"} {
		if _, _, _, err := h.PublishVersion(ctx, id, domain.PublishVersion{
			FlowID: flowID, Graph: flowtest.ConstGraph(result),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Deploy(ctx, id, domain.DeployVersion{
		FlowID: flowID, Environment: "sandbox", Version: 1,
	}); err != nil {
		t.Fatal(err)
	}

	rebuild := func() (flows.FlowView, []schedule.View) {
		st := store.NewMemory()
		if err := projection.New(log, st, flows.Projector{}, schedule.Projector{}).Start(ctx); err != nil {
			t.Fatal(err)
		}
		fv, ok, err := flows.Read(ctx, st, id, flowID)
		if err != nil || !ok {
			t.Fatalf("read flow: ok=%v err=%v", ok, err)
		}
		schedules, err := schedule.List(ctx, st, id, flowID)
		if err != nil {
			t.Fatal(err)
		}
		return fv, schedules
	}

	// Canceling an active time-box restores the version captured at activation,
	// in the same event that closes the lifecycle.
	activeID, _, err := h.ScheduleDeploy(ctx, id, flowID, "sandbox", 2, now.Add(time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ActivateSchedule(ctx, scheduler, activeID, flowID, "sandbox", 2, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.CancelSchedule(ctx, id, flowID, activeID, "maintenance ended early"); err != nil {
		t.Fatal(err)
	}
	fv, schedules := rebuild()
	if fv.Deployments["sandbox"].Version != 1 || schedules[0].Status != schedule.StatusCanceled {
		t.Fatalf("active cancel was not atomic: deployments=%+v schedule=%+v", fv.Deployments, schedules[0])
	}
	if _, err := h.CancelSchedule(ctx, id, flowID, activeID, "again"); err == nil {
		t.Fatal("terminal schedule accepted a second cancellation")
	}

	// With no pre-existing deployment, canceling an active schedule undeploys the
	// environment instead of leaving the supposedly temporary version live.
	emptyID, _, err := h.ScheduleDeploy(ctx, id, flowID, "staging", 1, now.Add(time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ActivateSchedule(ctx, scheduler, emptyID, flowID, "staging", 1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.CancelSchedule(ctx, id, flowID, emptyID, "remove temporary staging"); err != nil {
		t.Fatal(err)
	}
	fv, _ = rebuild()
	if _, live := fv.Deployments["staging"]; live {
		t.Fatalf("active cancel with no prior version left staging deployed: %+v", fv.Deployments)
	}

	// A later deliberate deployment owns the environment. Closing the older
	// schedule must not overwrite it with the schedule's captured baseline.
	supersededID, _, err := h.ScheduleDeploy(ctx, id, flowID, "sandbox", 2, now.Add(time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ActivateSchedule(ctx, scheduler, supersededID, flowID, "sandbox", 2, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Deploy(ctx, id, domain.DeployVersion{
		FlowID: flowID, Environment: "sandbox", Version: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.CancelSchedule(ctx, id, flowID, supersededID, "superseded"); err != nil {
		t.Fatal(err)
	}
	fv, schedules = rebuild()
	if fv.Deployments["sandbox"].Version != 3 || schedules[0].Status != schedule.StatusCanceled {
		t.Fatalf("cancel overwrote a newer deployment: deployments=%+v schedule=%+v", fv.Deployments, schedules[0])
	}
}
