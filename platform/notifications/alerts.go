// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications

import (
	"context"
	"encoding/json"
	"fmt"

	deevents "github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/monitor"
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
	if err := resolveApproval(ctx, e, s, p.RequestID); err != nil {
		return err
	}
	return shared(
		ctx, e, s, OperatorQueue, KindAlert, "model", p.Name,
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
