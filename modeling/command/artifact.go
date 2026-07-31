// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	decisionevents "github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/modeling/domain"
	"github.com/e6qu/intraktible/modeling/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

type artifactState struct {
	exists bool
	owner  string
	stage  domain.ArtifactStage
}

// RegisterExternalArtifact verifies and records external supply-chain metadata
// without fetching or deserializing the artifact.
func (h *Handler) RegisterExternalArtifact(
	ctx context.Context,
	id identity.Identity,
	registration domain.ArtifactRegistration,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := registration.Validate(h.now().UTC()); err != nil {
		return eventlog.Envelope{}, err
	}
	state, err := h.foldArtifact(ctx, id, registration.ArtifactID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.exists {
		return eventlog.Envelope{}, fmt.Errorf(
			"modeling: artifact %q already exists", registration.ArtifactID,
		)
	}
	return h.appendUnique(
		ctx, id, events.TypeArtifactRegistered,
		events.ArtifactRegistered{Registration: registration},
		"modeling.artifact.register\x00"+registration.ArtifactID,
	)
}

// ChangeArtifactStage applies an ordered, independently authorized promotion
// or terminal archive.
func (h *Handler) ChangeArtifactStage(
	ctx context.Context,
	id identity.Identity,
	artifactID string,
	to domain.ArtifactStage,
	reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(artifactID) == "" || strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, errors.New(
			"modeling: artifact_id and stage-change reason are required",
		)
	}
	state, err := h.foldArtifact(ctx, id, artifactID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !state.exists {
		return eventlog.Envelope{}, fmt.Errorf("modeling: unknown artifact %q", artifactID)
	}
	if err := domain.ValidateArtifactTransition(state.stage, to); err != nil {
		return eventlog.Envelope{}, err
	}
	if to != domain.ArtifactArchived && id.Actor == state.owner {
		return eventlog.Envelope{}, errors.New(
			"modeling: artifact promotion requires an independent actor",
		)
	}
	return h.appendUnique(
		ctx, id, events.TypeArtifactStageChanged,
		events.ArtifactStageChanged{
			ArtifactID: artifactID, From: state.stage, To: to,
			Reason: strings.TrimSpace(reason),
		},
		"modeling.artifact.stage\x00"+artifactID+"\x00"+string(state.stage),
	)
}

func (h *Handler) foldArtifact(
	ctx context.Context,
	id identity.Identity,
	artifactID string,
) (artifactState, error) {
	modelingEvents, err := h.log.ReadTenantStream(
		ctx, id.Org, id.Workspace, events.StreamModeling, 0,
	)
	if err != nil {
		return artifactState{}, err
	}
	modelEvents, err := h.log.ReadTenantStream(
		ctx, id.Org, id.Workspace, decisionevents.StreamModels, 0,
	)
	if err != nil {
		return artifactState{}, err
	}
	all := make([]eventlog.Envelope, 0, len(modelingEvents)+len(modelEvents))
	all = append(all, modelingEvents...)
	all = append(all, modelEvents...)
	sort.Slice(all, func(i, j int) bool { return all[i].Seq < all[j].Seq })
	var state artifactState
	for _, envelope := range all {
		switch envelope.Type {
		case events.TypeArtifactRegistered:
			var payload events.ArtifactRegistered
			if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
				return artifactState{}, err
			}
			if payload.Registration.ArtifactID == artifactID {
				state = artifactState{
					exists: true, owner: envelope.Actor, stage: domain.ArtifactRegistered,
				}
			}
		case decisionevents.TypeModelDefined:
			var payload decisionevents.ModelDefined
			if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
				return artifactState{}, err
			}
			if payload.Lineage != nil && payload.Training != nil &&
				payload.Lineage.ArtifactID == artifactID {
				state = artifactState{
					exists: true, owner: envelope.Actor, stage: domain.ArtifactRegistered,
				}
			}
		case events.TypeArtifactStageChanged:
			var payload events.ArtifactStageChanged
			if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
				return artifactState{}, err
			}
			if payload.ArtifactID == artifactID && state.exists {
				if payload.From != state.stage {
					return artifactState{}, fmt.Errorf(
						"modeling: artifact %q stage history diverged at seq %d",
						artifactID, envelope.Seq,
					)
				}
				state.stage = payload.To
			}
		}
	}
	return state, nil
}
