// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications_test

import (
	"context"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/authoring"
	deevents "github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/experiments"
	"github.com/e6qu/intraktible/decision-engine/monitor"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/notifications"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestChangeSetReviewReminderTargetsAssigneeAndReviewResolvesWork(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	maker := identity.Identity{Org: "demo", Workspace: "main", Actor: "maker"}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	emit := func(typ string, payload any, actor string) {
		if _, err := eventlog.AppendJSON(
			ctx, log, maker.Org, maker.Workspace, actor,
			authoring.Stream, typ, now, payload,
		); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	submitted := authoring.ChangeSetSubmitted{
		ChangeSetID: "cs-1", FlowID: "flow-1", Title: "Raise threshold",
		CreatedBy: "maker", Reviewers: []string{"checker"},
	}
	emit(authoring.TypeChangeSetSubmitted, submitted, "maker")
	emit(
		authoring.TypeChangeSetReviewReminded,
		authoring.ChangeSetReviewReminded(submitted),
		"authoring-scheduler",
	)

	st := store.NewMemory()
	if _, err := projection.New(log, st, notifications.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	checker := identity.Identity{Org: maker.Org, Workspace: maker.Workspace, Actor: "checker"}
	inbox, err := notifications.List(ctx, st, checker, notifications.Access{})
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 2 || inbox[0].ActionID != "cs-1" ||
		inbox[0].SubjectID != "flow-1:cs-1" {
		t.Fatalf("reviewer reminder inbox = %+v", inbox)
	}

	emit(authoring.TypeChangeSetReviewed, authoring.ChangeSetReviewed{
		ChangeSetID: "cs-1", FlowID: "flow-1", Title: "Raise threshold",
		CreatedBy: "maker", Decision: authoring.ReviewApprove,
	}, "checker")
	if _, err := projection.New(log, st, notifications.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	inbox, err = notifications.List(ctx, st, checker, notifications.Access{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range inbox {
		if !item.Resolved {
			t.Fatalf("terminal review left actionable reminder: %+v", item)
		}
	}
	creator, err := notifications.List(ctx, st, maker, notifications.Access{})
	if err != nil || len(creator) != 1 ||
		creator[0].Snippet != "Approved and ready to publish: Raise threshold" {
		t.Fatalf("creator review alert = %+v err=%v", creator, err)
	}
}

func TestOperationalAndApprovalAlertsReachRoleQueues(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	sys := identity.Identity{Org: "demo", Workspace: "main", Actor: "scheduler"}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	emit := func(stream, typ string, payload any) {
		if _, err := eventlog.AppendJSON(ctx, log, sys.Org, sys.Workspace, sys.Actor,
			stream, typ, now, payload); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}

	emit(monitor.StreamMonitors, monitor.TypeAlerted,
		monitor.Alerted{MonitorID: "mon-1", FlowID: "flow-1"})
	emit(monitor.StreamMonitors, monitor.TypeResolved,
		monitor.Resolved{MonitorID: "mon-1", FlowID: "flow-1"})
	emit(deevents.StreamModels, deevents.TypeModelDriftAlerted,
		deevents.ModelDriftAlerted{Name: "risk-model", PSI: 0.4, Threshold: 0.2})
	at := now.Add(time.Hour)
	emit(deevents.StreamFlows, deevents.TypeDeploymentRequested,
		deevents.DeploymentRequested{
			RequestID: "req-1", FlowID: "flow-1", Environment: "production", Version: 3, At: &at,
		})
	emit(deevents.StreamModels, deevents.TypeModelApprovalRequested,
		deevents.ModelApprovalRequested{
			RequestID: "model-req-1", Name: "risk-model", Version: 2,
		})
	emit(policy.StreamPolicies, policy.TypePolicyApprovalRequested,
		policy.ApprovalRequested{
			RequestID: "policy-req-1", PolicyID: "policy-1", Version: 4,
		})
	emit(experiments.Stream, experiments.TypeLaunchRequested,
		experiments.LaunchRequested{
			RequestID: "experiment-req-1", ExperimentID: "experiment-1", Cohort: 2,
		})
	emit(deevents.StreamFlows, deevents.TypeDeployScheduleActivated,
		deevents.DeployScheduleActivated{
			ScheduleID: "sch-1", FlowID: "flow-1", Environment: "production", Version: 3, PriorVersion: 2,
		})
	emit(deevents.StreamFlows, deevents.TypeDeployScheduleReverted,
		deevents.DeployScheduleReverted{
			ScheduleID: "sch-1", FlowID: "flow-1", Environment: "production", Version: 2, FromVersion: 3,
		})

	st := store.NewMemory()
	if err := projection.New(log, st, notifications.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	operator := identity.Identity{Org: sys.Org, Workspace: sys.Workspace, Actor: "operator"}
	ops, err := notifications.List(ctx, st, operator, notifications.Access{OperatorAlerts: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 5 {
		t.Fatalf("operator alerts = %d, want 5: %+v", len(ops), ops)
	}
	for _, n := range ops {
		if n.Kind != notifications.KindAlert || n.Recipient != "operator" ||
			(n.SubjectType != "flow" && n.SubjectType != "model") {
			t.Fatalf("unexpected operational alert: %+v", n)
		}
	}

	approver := identity.Identity{Org: sys.Org, Workspace: sys.Workspace, Actor: "approver"}
	approvals, err := notifications.List(ctx, st, approver, notifications.Access{Approvals: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(approvals) != 4 {
		t.Fatalf("approval queue wrong: %+v", approvals)
	}
	got := map[string]string{}
	for _, approval := range approvals {
		if approval.Kind != notifications.KindApproval {
			t.Fatalf("non-approval in approval queue: %+v", approval)
		}
		got[approval.SubjectType] = approval.SubjectID
	}
	if got["flow"] != "flow-1" || got["model"] != "risk-model" ||
		got["policy"] != "policy-1" || got["experiment"] != "experiment-1" {
		t.Fatalf("approval subjects = %+v", got)
	}
	if hidden, err := notifications.List(ctx, st,
		identity.Identity{Org: sys.Org, Workspace: sys.Workspace, Actor: "viewer"},
		notifications.Access{}); err != nil || len(hidden) != 0 {
		t.Fatalf("viewer saw shared alerts: %+v err=%v", hidden, err)
	}
}

func TestTerminalApprovalRetiresSharedWork(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "checker"}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	emit := func(stream, typ string, payload any) {
		if _, err := eventlog.AppendJSON(ctx, log, id.Org, id.Workspace, id.Actor,
			stream, typ, now, payload); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}

	emit(deevents.StreamFlows, deevents.TypeDeploymentRequested, deevents.DeploymentRequested{
		RequestID: "flow-approved", FlowID: "flow-1", Environment: "production", Version: 3,
	})
	emit(deevents.StreamFlows, deevents.TypeDeploymentApproved, deevents.DeploymentApproved{
		RequestID: "flow-approved", FlowID: "flow-1", Environment: "production", Version: 3,
	})
	emit(deevents.StreamModels, deevents.TypeModelApprovalRequested, deevents.ModelApprovalRequested{
		RequestID: "model-rejected", Name: "risk-model", Version: 2,
	})
	emit(deevents.StreamModels, deevents.TypeModelApprovalRejected, deevents.ModelApprovalRejected{
		RequestID: "model-rejected", Name: "risk-model", Version: 2, Reason: "validation failed",
	})
	emit(policy.StreamPolicies, policy.TypePolicyApprovalRequested, policy.ApprovalRequested{
		RequestID: "policy-approved", PolicyID: "policy-1", Version: 4,
	})
	emit(policy.StreamPolicies, policy.TypePolicyApprovalApproved, policy.ApprovalApproved{
		RequestID: "policy-approved", PolicyID: "policy-1", Version: 4, Reason: "bands reviewed",
	})
	emit(experiments.Stream, experiments.TypeLaunchRequested, experiments.LaunchRequested{
		RequestID: "experiment-approved", ExperimentID: "experiment-1", Cohort: 1,
	})
	emit(experiments.Stream, experiments.TypeLaunchApproved, experiments.LaunchApproved{
		RequestID: "experiment-approved", ExperimentID: "experiment-1", Cohort: 1,
	})

	st := store.NewMemory()
	if err := projection.New(log, st, notifications.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	inbox, err := notifications.List(ctx, st,
		identity.Identity{Org: id.Org, Workspace: id.Workspace, Actor: "other-approver"},
		notifications.Access{Approvals: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 0 {
		t.Fatalf("terminal approvals remained actionable: %+v", inbox)
	}
}
