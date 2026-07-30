// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/e6qu/intraktible/decision-engine/authoring"
	deevents "github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/experiments"
	"github.com/e6qu/intraktible/decision-engine/monitor"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/store"
)

// Shared alert queues are personalized by List, so every authorized user has an
// independent read receipt.
const (
	OperatorQueue = "@operators"
	ApproverQueue = "@approvers"
)

func shared(
	ctx context.Context,
	e eventlog.Envelope,
	s store.Store,
	recipient string,
	kind Kind,
	subjectType string,
	subjectID string,
	message string,
	actionID ...string,
) error {
	nid := notificationID(recipient, e.ID)
	action := ""
	if len(actionID) > 0 {
		action = actionID[0]
	}
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, nid), View{
		Org: e.Org, Workspace: e.Workspace, NotificationID: nid, Recipient: recipient,
		Kind: kind, SubjectType: subjectType, SubjectID: subjectID, Snippet: message,
		ActionID: action, Author: e.Actor, CreatedAt: e.Time, Seq: e.Seq,
	})
}

func applyMonitorAlerted(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p monitor.Alerted
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode monitor_alerted seq %d: %w", e.Seq, err)
	}
	return shared(ctx, e, s, OperatorQueue, KindAlert, "flow", p.FlowID,
		fmt.Sprintf("Flow monitor %s is firing", p.MonitorID))
}

func applyMonitorResolved(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p monitor.Resolved
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode monitor_resolved seq %d: %w", e.Seq, err)
	}
	return shared(ctx, e, s, OperatorQueue, KindAlert, "flow", p.FlowID,
		fmt.Sprintf("Flow monitor %s recovered", p.MonitorID))
}

func applyModelDriftAlerted(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p deevents.ModelDriftAlerted
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode model_drift_alerted seq %d: %w", e.Seq, err)
	}
	return shared(ctx, e, s, OperatorQueue, KindAlert, "model", p.Name,
		fmt.Sprintf("Model drift alert: PSI %.4g crossed %.4g", p.PSI, p.Threshold))
}

func applyModelDriftResolved(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p deevents.ModelDriftResolved
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode model_drift_resolved seq %d: %w", e.Seq, err)
	}
	return shared(ctx, e, s, OperatorQueue, KindAlert, "model", p.Name,
		"Model drift returned below its threshold")
}

func applyDeploymentRequested(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p deevents.DeploymentRequested
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode deployment_requested seq %d: %w", e.Seq, err)
	}
	message := fmt.Sprintf("Approval requested: deploy v%d to %s", p.Version, p.Environment)
	if p.At != nil {
		message = fmt.Sprintf("Approval requested: schedule v%d to %s at %s", p.Version, p.Environment, p.At.Format("2006-01-02 15:04Z"))
	}
	return shared(ctx, e, s, ApproverQueue, KindApproval, "flow", p.FlowID, message, p.RequestID)
}

func applyChangeSetSubmitted(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var payload authoring.ChangeSetSubmitted
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode changeset submitted seq %d: %w", e.Seq, err)
	}
	if payload.ChangeSetID == "" || payload.FlowID == "" || payload.CreatedBy == "" {
		return fmt.Errorf("notifications: changeset submitted seq %d lacks routing identity", e.Seq)
	}
	return deliverChangeSetReviewTask(
		ctx, e, s, authoring.ChangeSetReviewReminded(payload), "Review requested",
	)
}

func applyChangeSetReviewed(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var payload authoring.ChangeSetReviewed
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode changeset reviewed seq %d: %w", e.Seq, err)
	}
	if payload.ChangeSetID == "" || payload.FlowID == "" || payload.CreatedBy == "" {
		return fmt.Errorf("notifications: changeset reviewed seq %d lacks routing identity", e.Seq)
	}
	if err := resolveApproval(ctx, e, s, payload.ChangeSetID); err != nil {
		return err
	}
	message := fmt.Sprintf("Changes requested: %s", payload.Title)
	if payload.Decision == authoring.ReviewApprove {
		message = fmt.Sprintf("Approved and ready to publish: %s", payload.Title)
	}
	return shared(
		ctx, e, s, payload.CreatedBy, KindAlert,
		"changeset", payload.FlowID+":"+payload.ChangeSetID, message,
	)
}

func applyChangeSetReviewReminded(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var payload authoring.ChangeSetReviewReminded
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode changeset reminder seq %d: %w", e.Seq, err)
	}
	if payload.ChangeSetID == "" || payload.FlowID == "" || payload.CreatedBy == "" {
		return fmt.Errorf("notifications: changeset reminder seq %d lacks routing identity", e.Seq)
	}
	return deliverChangeSetReviewTask(ctx, e, s, payload, "Review overdue")
}

func deliverChangeSetReviewTask(
	ctx context.Context,
	e eventlog.Envelope,
	s store.Store,
	payload authoring.ChangeSetReviewReminded,
	prefix string,
) error {
	subjectID := payload.FlowID + ":" + payload.ChangeSetID
	message := fmt.Sprintf("%s: %s", prefix, payload.Title)
	if len(payload.Reviewers) == 0 {
		return shared(
			ctx, e, s, ApproverQueue, KindApproval,
			"changeset", subjectID, message, payload.ChangeSetID,
		)
	}
	for _, reviewer := range payload.Reviewers {
		if reviewer == "" || reviewer == payload.CreatedBy {
			return fmt.Errorf("notifications: invalid reviewer on changeset %q", payload.ChangeSetID)
		}
		if err := shared(
			ctx, e, s, reviewer, KindApproval,
			"changeset", subjectID, message, payload.ChangeSetID,
		); err != nil {
			return err
		}
	}
	return nil
}

func applyDeploymentApproved(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p deevents.DeploymentApproved
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode deployment_approved seq %d: %w", e.Seq, err)
	}
	if err := resolveApproval(ctx, e, s, p.RequestID); err != nil {
		return err
	}
	message := fmt.Sprintf("Deployment approved: v%d is live in %s", p.Version, p.Environment)
	if p.ScheduleID != "" && p.At != nil {
		message = fmt.Sprintf("Scheduled deployment approved: v%d to %s at %s", p.Version, p.Environment, p.At.Format("2006-01-02 15:04Z"))
	}
	return shared(ctx, e, s, OperatorQueue, KindAlert, "flow", p.FlowID, message)
}

func applyDeploymentRejected(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p deevents.DeploymentRejected
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode deployment_rejected seq %d: %w", e.Seq, err)
	}
	return resolveApproval(ctx, e, s, p.RequestID)
}

func applyExperimentLaunchRequested(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p experiments.LaunchRequested
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode experiment launch requested seq %d: %w", e.Seq, err)
	}
	return shared(
		ctx, e, s, ApproverQueue, KindApproval, "experiment", p.ExperimentID,
		fmt.Sprintf("Approval requested: production experiment cohort %d", p.Cohort), p.RequestID,
	)
}

func applyExperimentLaunchApproved(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p experiments.LaunchApproved
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode experiment launch approved seq %d: %w", e.Seq, err)
	}
	return resolveAndAlert(
		ctx, e, s, p.RequestID, "experiment", p.ExperimentID,
		fmt.Sprintf("Production experiment cohort %d started", p.Cohort),
	)
}

func applyExperimentLaunchRejected(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p experiments.LaunchRejected
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode experiment launch rejected seq %d: %w", e.Seq, err)
	}
	return resolveApproval(ctx, e, s, p.RequestID)
}

func applyModelApprovalRequested(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p deevents.ModelApprovalRequested
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode model_approval_requested seq %d: %w", e.Seq, err)
	}
	return shared(
		ctx, e, s, ApproverQueue, KindApproval, "model", p.Name,
		fmt.Sprintf("Approval requested: model %s v%d", p.Name, p.Version), p.RequestID,
	)
}

func applyModelApprovalApproved(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p deevents.ModelApprovalApproved
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode model_approval_approved seq %d: %w", e.Seq, err)
	}
	return resolveAndAlert(
		ctx, e, s, p.RequestID, "model", p.Name,
		fmt.Sprintf("Model approved: %s v%d", p.Name, p.Version),
	)
}

func applyModelApprovalRejected(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p deevents.ModelApprovalRejected
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode model_approval_rejected seq %d: %w", e.Seq, err)
	}
	return resolveApproval(ctx, e, s, p.RequestID)
}

func applyPolicyApprovalRequested(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p policy.ApprovalRequested
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode policy approval requested seq %d: %w", e.Seq, err)
	}
	label := p.Name
	if label == "" {
		label = p.PolicyID
	}
	return shared(
		ctx, e, s, ApproverQueue, KindApproval, "policy", p.PolicyID,
		fmt.Sprintf("Approval requested: policy %s v%d", label, p.Version), p.RequestID,
	)
}

func applyPolicyApprovalApproved(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p policy.ApprovalApproved
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode policy approval approved seq %d: %w", e.Seq, err)
	}
	return resolveAndAlert(
		ctx, e, s, p.RequestID, "policy", p.PolicyID,
		fmt.Sprintf("Policy approved for non-sandbox serving: v%d", p.Version),
	)
}

func resolveAndAlert(
	ctx context.Context,
	e eventlog.Envelope,
	s store.Store,
	requestID, resourceType, resourceID, message string,
) error {
	if err := resolveApproval(ctx, e, s, requestID); err != nil {
		return err
	}
	return shared(
		ctx, e, s, OperatorQueue, KindAlert, resourceType, resourceID, message,
	)
}

func applyPolicyApprovalRejected(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p policy.ApprovalRejected
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode policy approval rejected seq %d: %w", e.Seq, err)
	}
	return resolveApproval(ctx, e, s, p.RequestID)
}

func resolveApproval(ctx context.Context, e eventlog.Envelope, s store.Store, actionID string) error {
	if actionID == "" {
		return fmt.Errorf("notifications: terminal approval seq %d has no request id", e.Seq)
	}
	all, err := store.ListDocs[View](ctx, s, Collection, store.Key(e.Org, e.Workspace, ""))
	if err != nil {
		return err
	}
	found := false
	for _, notification := range all {
		if notification.Kind != KindApproval || notification.ActionID != actionID || notification.Resolved {
			continue
		}
		found = true
		ok, err := store.UpdateDoc(
			ctx, s, Collection,
			store.Key(e.Org, e.Workspace, notification.NotificationID),
			func(v *View) { v.Resolved = true },
		)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("notifications: approval %q disappeared while resolving request %q", notification.NotificationID, actionID)
		}
	}
	if !found {
		return fmt.Errorf("notifications: terminal approval seq %d for unknown request %q", e.Seq, actionID)
	}
	return nil
}

func applyFlowRolledBack(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p deevents.FlowVersionRolledBack
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode flow_rolled_back seq %d: %w", e.Seq, err)
	}
	return shared(ctx, e, s, OperatorQueue, KindAlert, "flow", p.FlowID,
		fmt.Sprintf("%s rolled back from v%d to v%d", p.Environment, p.FromVersion, p.Version))
}

func applyScheduleActivated(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p deevents.DeployScheduleActivated
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode schedule_activated seq %d: %w", e.Seq, err)
	}
	// Legacy marker events have no deployment details and their historical
	// companion event already describes the live change.
	if p.Environment == "" || p.Version == 0 {
		return nil
	}
	return shared(ctx, e, s, OperatorQueue, KindAlert, "flow", p.FlowID,
		fmt.Sprintf("Scheduled deployment activated: v%d in %s", p.Version, p.Environment))
}

func applyScheduleReverted(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p deevents.DeployScheduleReverted
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("notifications: decode schedule_reverted seq %d: %w", e.Seq, err)
	}
	if p.Environment == "" {
		return nil
	}
	message := fmt.Sprintf("Scheduled deployment window ended in %s", p.Environment)
	if p.Version > 0 {
		message = fmt.Sprintf("Scheduled deployment reverted %s from v%d to v%d", p.Environment, p.FromVersion, p.Version)
	}
	return shared(ctx, e, s, OperatorQueue, KindAlert, "flow", p.FlowID, message)
}
