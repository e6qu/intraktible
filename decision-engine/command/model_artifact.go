// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"encoding/json"
	"fmt"

	modelingdomain "github.com/e6qu/intraktible/modeling/domain"
	modelingevents "github.com/e6qu/intraktible/modeling/events"
	"github.com/e6qu/intraktible/platform/identity"
)

// requireTrainedArtifactStage folds the artifact supply-chain transitions from
// the authoritative log. A trained model starts at registered in its
// ModelDefined fact; every subsequent transition must join that exact artifact
// and preserve the ordered stage history.
func (h *Handler) requireTrainedArtifactStage(
	ctx context.Context,
	id identity.Identity,
	artifactID string,
	required modelingdomain.ArtifactStage,
) error {
	envelopes, err := h.log.ReadTenantStream(
		ctx, id.Org, id.Workspace, modelingevents.StreamModeling, 0,
	)
	if err != nil {
		return err
	}
	stage := modelingdomain.ArtifactRegistered
	for _, envelope := range envelopes {
		if envelope.Type != modelingevents.TypeArtifactStageChanged {
			continue
		}
		var payload modelingevents.ArtifactStageChanged
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			return fmt.Errorf(
				"decision-engine: decode artifact stage seq %d: %w", envelope.Seq, err,
			)
		}
		if payload.ArtifactID != artifactID {
			continue
		}
		if payload.From != stage {
			return fmt.Errorf(
				"decision-engine: artifact %q stage history diverged at seq %d",
				artifactID, envelope.Seq,
			)
		}
		if err := modelingdomain.ValidateArtifactTransition(payload.From, payload.To); err != nil {
			return err
		}
		stage = payload.To
	}
	if stage == modelingdomain.ArtifactArchived {
		return fmt.Errorf("decision-engine: trained artifact %q is archived", artifactID)
	}
	switch required {
	case modelingdomain.ArtifactValidated:
		if stage == modelingdomain.ArtifactValidated ||
			stage == modelingdomain.ArtifactProduction {
			return nil
		}
	case modelingdomain.ArtifactProduction:
		if stage == modelingdomain.ArtifactProduction {
			return nil
		}
	default:
		return fmt.Errorf("decision-engine: unsupported required artifact stage %q", required)
	}
	return fmt.Errorf(
		"decision-engine: trained artifact %q must be %s; current stage is %s",
		artifactID, required, stage,
	)
}
