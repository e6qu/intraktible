// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications

import (
	"context"
	"encoding/json"
	"fmt"

	contextevents "github.com/e6qu/intraktible/context-layer/events"
	modelingevents "github.com/e6qu/intraktible/modeling/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/store"
)

func applyEntityQualityIncident(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload contextevents.EntityRecorded
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode quality entity seq %d: %w", event.Seq, err)
	}
	return applyRecordQualityIncident(
		ctx, event, st, payload.EntityType, "", payload.SchemaEvidence,
	)
}

func applyEventQualityIncident(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload contextevents.EventRecorded
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode quality event seq %d: %w", event.Seq, err)
	}
	return applyRecordQualityIncident(
		ctx, event, st, payload.EntityType, payload.EventName, payload.SchemaEvidence,
	)
}

func applyRecordQualityIncident(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
	entityType string,
	eventName string,
	evidence contextevents.SchemaEvidence,
) error {
	if len(evidence.Violations) == 0 ||
		(evidence.Action != "refer" && evidence.Action != "approved_stale") {
		return nil
	}
	source := entityType
	if eventName != "" {
		source += "/" + eventName
	}
	return shared(
		ctx, event, st, OperatorQueue, KindAlert, "quality_incident", event.ID,
		fmt.Sprintf(
			"Source-quality incident on %s: %d governed violation(s) require triage",
			source, len(evidence.Violations),
		),
		event.ID,
	)
}

func applyFreshnessQualityIncident(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload modelingevents.SourceFreshnessViolated
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode freshness incident seq %d: %w", event.Seq, err)
	}
	return shared(
		ctx, event, st, OperatorQueue, KindAlert,
		"quality_incident", payload.IncidentID,
		fmt.Sprintf("Source freshness incident on %s requires triage", payload.Ref.Key()),
		payload.IncidentID,
	)
}

func applyQualityIncidentAcknowledged(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload modelingevents.QualityIncidentAcknowledged
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode quality acknowledgement seq %d: %w", event.Seq, err)
	}
	return updateQualityAlert(ctx, event, st, payload.IncidentID, func(view *View) {
		view.Snippet = fmt.Sprintf("Quality incident acknowledged by %s: %s", event.Actor, payload.Note)
	})
}

func applyQualityIncidentResolved(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload modelingevents.QualityIncidentResolved
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode quality resolution seq %d: %w", event.Seq, err)
	}
	return resolveQualityAlert(ctx, event, st, payload.IncidentID)
}

func applyFreshnessQualityRecovered(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload modelingevents.SourceFreshnessRecovered
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("notifications: decode freshness recovery seq %d: %w", event.Seq, err)
	}
	return resolveQualityAlert(ctx, event, st, payload.IncidentID)
}

func resolveQualityAlert(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
	incidentID string,
) error {
	return updateQualityAlert(ctx, event, st, incidentID, func(view *View) {
		view.Resolved = true
	})
}

func updateQualityAlert(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
	incidentID string,
	update func(*View),
) error {
	notifications, err := store.ListDocs[View](
		ctx, st, Collection, store.Key(event.Org, event.Workspace, OperatorQueue+":"),
	)
	if err != nil {
		return err
	}
	for _, notification := range notifications {
		if notification.SubjectType != "quality_incident" ||
			notification.SubjectID != incidentID ||
			notification.ActionID != incidentID {
			continue
		}
		updated, updateErr := store.UpdateDoc(
			ctx, st, Collection,
			store.Key(event.Org, event.Workspace, notification.NotificationID),
			update,
		)
		if updateErr != nil {
			return updateErr
		}
		if !updated {
			return fmt.Errorf(
				"notifications: quality alert %q disappeared during lifecycle update",
				notification.NotificationID,
			)
		}
		return nil
	}
	return fmt.Errorf(
		"notifications: quality incident %q has no operator alert at seq %d",
		incidentID, event.Seq,
	)
}
