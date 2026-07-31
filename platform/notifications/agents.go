// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	agentgovernance "github.com/e6qu/intraktible/agent-manager/governance"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/store"
)

const agentAssistIndexCollection = "notification_agent_assist_index"

type agentAssistIndex struct {
	AssistID        string `json:"assist_id"`
	CaseID          string `json:"case_id"`
	TemplateID      string `json:"template_id"`
	Release         int    `json:"release"`
	RequestedBy     string `json:"requested_by"`
	PolicyRequested bool   `json:"policy_requested,omitempty"`
}

func (i agentAssistIndex) alertRecipient() string {
	if i.PolicyRequested {
		return OperatorQueue
	}
	return i.RequestedBy
}

func agentReleaseSubject(templateID string, release int) string {
	return templateID + ":" + strconv.Itoa(release)
}

func applyAgentCampaignRecorded(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.CampaignRecorded
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode agent campaign seq %d: %w", event.Seq, err)
	}
	if !payload.Result.Blocking {
		return nil
	}
	return shared(
		ctx, event, st, OperatorQueue, KindAlert, "agent_release",
		agentReleaseSubject(payload.Result.TemplateID, payload.Result.Release),
		fmt.Sprintf(
			"Agent release blocked by evaluation suite %s@%d",
			payload.Result.SuiteID, payload.Result.SuiteVersion,
		),
		payload.Result.CampaignID,
	)
}

func applyAgentCampaignTrialAdjudicated(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.CampaignTrialAdjudicated
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf(
			"notifications: decode agent campaign adjudication seq %d: %w",
			event.Seq, err,
		)
	}
	switch {
	case payload.PreviousAssessment.Blocking && !payload.Assessment.Blocking:
		return resolveAgentCampaignAlerts(
			ctx, event, st, payload.Adjudication.CampaignID,
		)
	case !payload.PreviousAssessment.Blocking && payload.Assessment.Blocking:
		return shared(
			ctx, event, st, OperatorQueue, KindAlert, "agent_release",
			agentReleaseSubject(payload.TemplateID, payload.Release),
			fmt.Sprintf(
				"Agent evaluation campaign %s became blocking after human adjudication",
				payload.Adjudication.CampaignID,
			),
			payload.Adjudication.CampaignID,
		)
	default:
		return nil
	}
}

func resolveAgentCampaignAlerts(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
	campaignID string,
) error {
	notifications, err := store.ListDocs[View](
		ctx, st, Collection, store.Key(event.Org, event.Workspace, ""),
	)
	if err != nil {
		return err
	}
	found := false
	for _, notification := range notifications {
		if notification.Kind != KindAlert || notification.ActionID != campaignID ||
			notification.Resolved {
			continue
		}
		found = true
		updated, err := store.UpdateDoc(
			ctx, st, Collection,
			store.Key(event.Org, event.Workspace, notification.NotificationID),
			func(view *View) { view.Resolved = true },
		)
		if err != nil {
			return err
		}
		if !updated {
			return fmt.Errorf(
				"notifications: campaign alert %q disappeared during adjudication",
				notification.NotificationID,
			)
		}
	}
	if !found {
		return fmt.Errorf(
			"notifications: campaign %q became non-blocking without a blocking alert",
			campaignID,
		)
	}
	return nil
}

func applyAgentReleaseReviewRequested(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.ReleaseReviewRequestedEvent
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode agent review request seq %d: %w", event.Seq, err)
	}
	subject := agentReleaseSubject(payload.TemplateID, payload.Release)
	message := fmt.Sprintf("Review requested: governed agent release %s", subject)
	if len(payload.Reviewers) == 0 {
		return shared(
			ctx, event, st, ApproverQueue, KindApproval, "agent_release",
			subject, message, payload.RequestID,
		)
	}
	for _, reviewer := range payload.Reviewers {
		if reviewer == "" || reviewer == payload.RequestedBy {
			return fmt.Errorf(
				"notifications: invalid reviewer on agent release %s", subject,
			)
		}
		if err := shared(
			ctx, event, st, reviewer, KindApproval, "agent_release",
			subject, message, payload.RequestID,
		); err != nil {
			return err
		}
	}
	return nil
}

func applyAgentReleaseReviewed(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.ReleaseReviewed
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode agent review decision seq %d: %w", event.Seq, err)
	}
	if err := resolveApproval(ctx, event, st, payload.RequestID); err != nil {
		return err
	}
	message := fmt.Sprintf(
		"Agent release %s: %s", payload.Decision,
		agentReleaseSubject(payload.TemplateID, payload.Release),
	)
	return shared(
		ctx, event, st, ApproverQueue, KindAlert, "agent_release",
		agentReleaseSubject(payload.TemplateID, payload.Release), message,
	)
}

func applyAgentReleaseReviewExpired(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.ReleaseReviewExpired
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode agent review expiry seq %d: %w", event.Seq, err)
	}
	if err := resolveApproval(ctx, event, st, payload.RequestID); err != nil {
		return err
	}
	return shared(
		ctx, event, st, OperatorQueue, KindAlert, "agent_release",
		agentReleaseSubject(payload.TemplateID, payload.Release),
		fmt.Sprintf(
			"Agent release review expired: %s",
			agentReleaseSubject(payload.TemplateID, payload.Release),
		),
	)
}

func applyAgentDeploymentRequested(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.DeploymentRequestedEvent
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode agent deployment request seq %d: %w", event.Seq, err)
	}
	return shared(
		ctx, event, st, OperatorQueue, KindAlert, "agent_deployment",
		payload.Request.DeploymentID,
		fmt.Sprintf(
			"Agent release %s scheduled for %s",
			agentReleaseSubject(payload.Request.TemplateID, payload.Request.Release),
			payload.Request.Environment,
		),
	)
}

func applyAgentDeploymentActivated(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.DeploymentActivatedEvent
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode agent deployment activation seq %d: %w", event.Seq, err)
	}
	return shared(
		ctx, event, st, OperatorQueue, KindAlert, "agent_deployment",
		payload.DeploymentID,
		fmt.Sprintf(
			"Agent release %s is active in %s",
			agentReleaseSubject(payload.TemplateID, payload.Release), payload.Environment,
		),
	)
}

func applyAgentDeploymentPaused(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.DeploymentPausedEvent
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode agent deployment pause seq %d: %w", event.Seq, err)
	}
	return shared(
		ctx, event, st, OperatorQueue, KindAlert, "agent_deployment",
		payload.DeploymentID, "Agent deployment paused: "+payload.Reason,
	)
}

func applyAgentDeploymentResumed(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.DeploymentResumedEvent
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode agent deployment resume seq %d: %w", event.Seq, err)
	}
	return shared(
		ctx, event, st, OperatorQueue, KindAlert, "agent_deployment",
		payload.DeploymentID, "Agent deployment resumed: "+payload.Reason,
	)
}

func applyAgentDeploymentRolledBack(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.DeploymentRolledBackEvent
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode agent deployment rollback seq %d: %w", event.Seq, err)
	}
	return shared(
		ctx, event, st, OperatorQueue, KindAlert, "agent_deployment",
		payload.DeploymentID,
		fmt.Sprintf(
			"Agent deployment rolled back from release %d to %d",
			payload.FromRelease, payload.ToRelease,
		),
	)
}

func applyAgentAssistRequested(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.AssistRequested
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode agent assist request seq %d: %w", event.Seq, err)
	}
	if payload.AssistID == "" || payload.CaseID == "" || payload.RequestedBy == "" {
		return fmt.Errorf("notifications: invalid agent assist request seq %d", event.Seq)
	}
	return store.PutDoc(
		ctx, st, agentAssistIndexCollection,
		store.Key(event.Org, event.Workspace, payload.AssistID),
		agentAssistIndex{
			AssistID: payload.AssistID, CaseID: payload.CaseID,
			TemplateID: payload.TemplateID, Release: payload.Release,
			RequestedBy: payload.RequestedBy, PolicyRequested: payload.PolicySource != nil,
		},
	)
}

func applyAgentAssistFailed(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.AssistFailed
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode agent assist failure seq %d: %w", event.Seq, err)
	}
	index, found, err := store.GetDoc[agentAssistIndex](
		ctx, st, agentAssistIndexCollection,
		store.Key(event.Org, event.Workspace, payload.AssistID),
	)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf(
			"notifications: assist failure seq %d references unknown request %q",
			event.Seq, payload.AssistID,
		)
	}
	return shared(
		ctx, event, st, index.alertRecipient(), KindAlert, "agent_assist",
		index.CaseID+":"+payload.AssistID, "Case assist failed: "+payload.Reason,
	)
}

func applyAgentAssistDeadLettered(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.AssistDeadLettered
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf(
			"notifications: decode agent assist dead letter seq %d: %w", event.Seq, err,
		)
	}
	index, found, err := readAgentAssistIndex(ctx, event, st, payload.AssistID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf(
			"notifications: assist dead letter seq %d references unknown request %q",
			event.Seq, payload.AssistID,
		)
	}
	return shared(
		ctx, event, st, index.alertRecipient(), KindAlert, "agent_assist",
		index.CaseID+":"+payload.AssistID,
		"Case assist lost its worker lease; outcome is indeterminate and retry needs acknowledgement",
	)
}

func applyAgentAssistRetryRequested(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.AssistRetryRequested
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf(
			"notifications: decode agent assist retry seq %d: %w", event.Seq, err,
		)
	}
	index, found, err := readAgentAssistIndex(ctx, event, st, payload.AssistID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf(
			"notifications: assist retry seq %d references unknown request %q",
			event.Seq, payload.AssistID,
		)
	}
	notifications, err := store.ListDocs[View](
		ctx, st, Collection,
		store.Key(event.Org, event.Workspace, index.alertRecipient()+":"),
	)
	if err != nil {
		return err
	}
	subject := index.CaseID + ":" + payload.AssistID
	resolved := false
	for _, notification := range notifications {
		if notification.SubjectType != "agent_assist" ||
			notification.SubjectID != subject || notification.Resolved {
			continue
		}
		updated, updateErr := store.UpdateDoc(
			ctx, st, Collection,
			store.Key(event.Org, event.Workspace, notification.NotificationID),
			func(view *View) { view.Resolved = true },
		)
		if updateErr != nil {
			return updateErr
		}
		if !updated {
			return fmt.Errorf(
				"notifications: assist alert %q disappeared during retry",
				notification.NotificationID,
			)
		}
		resolved = true
	}
	if !resolved {
		return fmt.Errorf(
			"notifications: retried assist %q had no actionable failure alert",
			payload.AssistID,
		)
	}
	return nil
}

func readAgentAssistIndex(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
	assistID string,
) (agentAssistIndex, bool, error) {
	return store.GetDoc[agentAssistIndex](
		ctx, st, agentAssistIndexCollection,
		store.Key(event.Org, event.Workspace, assistID),
	)
}

func applyAgentToolApprovalRequested(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.ToolApprovalRequested
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode agent tool approval seq %d: %w", event.Seq, err)
	}
	return shared(
		ctx, event, st, ApproverQueue, KindApproval, "agent_tool_approval",
		payload.ApprovalID,
		fmt.Sprintf(
			"Tool %s requires approval before case assist execution", payload.Name,
		),
		payload.ApprovalID,
	)
}

func applyAgentToolApprovalDecided(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.ToolApprovalDecided
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf(
			"notifications: decode agent tool approval decision seq %d: %w",
			event.Seq, err,
		)
	}
	if err := resolveApproval(ctx, event, st, payload.ApprovalID); err != nil {
		return err
	}
	index, found, err := store.GetDoc[agentAssistIndex](
		ctx, st, agentAssistIndexCollection,
		store.Key(event.Org, event.Workspace, payload.AssistID),
	)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf(
			"notifications: tool approval decision seq %d references unknown assist %q",
			event.Seq, payload.AssistID,
		)
	}
	return shared(
		ctx, event, st, index.alertRecipient(), KindAlert, "agent_assist",
		index.CaseID+":"+payload.AssistID,
		fmt.Sprintf("Tool approval %s: %s", payload.Decision, payload.Reason),
	)
}

func applyAgentToolApprovalExpired(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.ToolApprovalExpired
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf(
			"notifications: decode agent tool approval expiry seq %d: %w",
			event.Seq, err,
		)
	}
	if err := resolveApproval(ctx, event, st, payload.ApprovalID); err != nil {
		return err
	}
	index, found, err := store.GetDoc[agentAssistIndex](
		ctx, st, agentAssistIndexCollection,
		store.Key(event.Org, event.Workspace, payload.AssistID),
	)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf(
			"notifications: tool approval expiry seq %d references unknown assist %q",
			event.Seq, payload.AssistID,
		)
	}
	return shared(
		ctx, event, st, index.alertRecipient(), KindAlert, "agent_assist",
		index.CaseID+":"+payload.AssistID, "Tool approval expired before execution",
	)
}

func applyAgentSafetyIncidentOpened(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.SafetyIncidentOpened
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode agent safety incident seq %d: %w", event.Seq, err)
	}
	return shared(
		ctx, event, st, OperatorQueue, KindAlert, "agent_incident", payload.IncidentID,
		fmt.Sprintf(
			"%s agent safety incident on %s: %s",
			payload.Severity, agentReleaseSubject(payload.TemplateID, payload.Release),
			payload.Summary,
		),
	)
}

func applyAgentSafetyIncidentResolved(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload agentgovernance.SafetyIncidentResolved
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode resolved agent incident seq %d: %w", event.Seq, err)
	}
	return shared(
		ctx, event, st, OperatorQueue, KindAlert, "agent_incident", payload.IncidentID,
		"Agent safety incident resolved: "+payload.Resolution,
	)
}
