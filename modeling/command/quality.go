// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	contextevents "github.com/e6qu/intraktible/context-layer/events"
	"github.com/e6qu/intraktible/modeling/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

type qualityIncidentState struct {
	found        bool
	acknowledged bool
	resolved     bool
}

// AcknowledgeQualityIncident records that an operator has accepted ownership
// of an actionable source-quality finding.
func (h *Handler) AcknowledgeQualityIncident(
	ctx context.Context,
	id identity.Identity,
	incidentID string,
	note string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(incidentID) == "" || strings.TrimSpace(note) == "" {
		return eventlog.Envelope{}, errors.New("modeling: incident_id and acknowledgement note are required")
	}
	state, err := h.qualityIncidentState(ctx, id, incidentID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	switch {
	case !state.found:
		return eventlog.Envelope{}, fmt.Errorf("modeling: unknown quality incident %q", incidentID)
	case state.resolved:
		return eventlog.Envelope{}, fmt.Errorf("modeling: quality incident %q is already resolved", incidentID)
	case state.acknowledged:
		return eventlog.Envelope{}, fmt.Errorf("modeling: quality incident %q is already acknowledged", incidentID)
	}
	return h.appendUnique(ctx, id, events.TypeQualityIncidentAcknowledged,
		events.QualityIncidentAcknowledged{
			IncidentID: incidentID, Note: strings.TrimSpace(note),
		},
		"modeling.quality.acknowledge\x00"+incidentID)
}

// ResolveQualityIncident closes a refer/approved-stale ingestion finding.
func (h *Handler) ResolveQualityIncident(
	ctx context.Context,
	id identity.Identity,
	incidentID string,
	reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(incidentID) == "" || strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, errors.New("modeling: incident_id and resolution reason are required")
	}
	state, err := h.qualityIncidentState(ctx, id, incidentID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	switch {
	case !state.found:
		return eventlog.Envelope{}, fmt.Errorf("modeling: unknown quality incident %q", incidentID)
	case state.resolved:
		return eventlog.Envelope{}, fmt.Errorf("modeling: quality incident %q is already resolved", incidentID)
	case !state.acknowledged:
		return eventlog.Envelope{}, fmt.Errorf(
			"modeling: quality incident %q must be acknowledged before manual resolution",
			incidentID,
		)
	}
	return h.appendUnique(ctx, id, events.TypeQualityIncidentResolved,
		events.QualityIncidentResolved{
			IncidentID: incidentID, Reason: strings.TrimSpace(reason),
		},
		"modeling.quality.resolve\x00"+incidentID)
}

func (h *Handler) qualityIncidentState(
	ctx context.Context,
	id identity.Identity,
	incidentID string,
) (qualityIncidentState, error) {
	contextEnvelopes, err := h.log.ReadTenantStream(
		ctx, id.Org, id.Workspace, contextevents.StreamContext, 0,
	)
	if err != nil {
		return qualityIncidentState{}, err
	}
	state := qualityIncidentState{}
	for _, envelope := range contextEnvelopes {
		if envelope.ID != incidentID {
			continue
		}
		switch envelope.Type {
		case contextevents.TypeEntityRecorded:
			var payload contextevents.EntityRecorded
			if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
				return qualityIncidentState{}, err
			}
			state.found = len(payload.SchemaEvidence.Violations) > 0 &&
				(payload.SchemaEvidence.Action == "refer" ||
					payload.SchemaEvidence.Action == "approved_stale")
		case contextevents.TypeEventRecorded:
			var payload contextevents.EventRecorded
			if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
				return qualityIncidentState{}, err
			}
			state.found = len(payload.SchemaEvidence.Violations) > 0 &&
				(payload.SchemaEvidence.Action == "refer" ||
					payload.SchemaEvidence.Action == "approved_stale")
		}
		break
	}
	modelingEnvelopes, err := h.log.ReadTenantStream(
		ctx, id.Org, id.Workspace, events.StreamModeling, 0,
	)
	if err != nil {
		return qualityIncidentState{}, err
	}
	for _, envelope := range modelingEnvelopes {
		switch envelope.Type {
		case events.TypeSourceFreshnessViolated:
			var payload events.SourceFreshnessViolated
			if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
				return qualityIncidentState{}, err
			}
			if payload.IncidentID == incidentID {
				state.found = true
			}
		case events.TypeQualityIncidentAcknowledged:
			var payload events.QualityIncidentAcknowledged
			if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
				return qualityIncidentState{}, err
			}
			if payload.IncidentID == incidentID {
				state.acknowledged = true
			}
		case events.TypeQualityIncidentResolved:
			var payload events.QualityIncidentResolved
			if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
				return qualityIncidentState{}, err
			}
			if payload.IncidentID == incidentID {
				state.resolved = true
			}
		case events.TypeSourceFreshnessRecovered:
			var payload events.SourceFreshnessRecovered
			if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
				return qualityIncidentState{}, err
			}
			if payload.IncidentID == incidentID {
				state.resolved = true
			}
		}
	}
	return state, nil
}
