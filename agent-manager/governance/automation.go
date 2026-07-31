// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/e6qu/intraktible/case-manager/cases"
	casedomain "github.com/e6qu/intraktible/case-manager/domain"
	engine "github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/platform/identity"
)

var (
	ErrAssistPolicyWaiting    = errors.New("agent governance: assist policy is waiting for governed evidence")
	ErrAssistPolicyIneligible = errors.New("agent governance: assist policy has no eligible active deployment")
)

// RequestPolicyAssist reconciles one immutable case-type or queue policy into a
// normal durable assist request. It resolves the active exact release at
// admission, seals the same minimal evidence snapshot as the reviewer path, and
// uses policy+evidence lineage for retry-safe idempotency.
func (h *Handler) RequestPolicyAssist(
	ctx context.Context,
	id identity.Identity,
	view cases.CaseView,
	policy casedomain.AssistAutomation,
	source AssistPolicySource,
) (string, error) {
	if err := id.Valid(); err != nil {
		return "", err
	}
	if err := policy.Validate(); err != nil {
		return "", err
	}
	if view.Org != id.Org || view.Workspace != id.Workspace ||
		strings.TrimSpace(view.CaseID) == "" {
		return "", errors.New("agent governance: policy assist case tenant lineage is invalid")
	}
	selected, evidenceIDs, evidenceSeq, err := policyEvidence(view, policy)
	if err != nil {
		return "", err
	}
	fingerprint, err := hashJSON(selected)
	if err != nil {
		return "", err
	}
	source.EvidenceFingerprint = fingerprint
	if err := source.Validate(); err != nil {
		return "", err
	}
	environment := engine.Environment(policy.Environment)
	h.mu.Lock()
	state, err := h.fold(ctx, id)
	if err != nil {
		h.mu.Unlock()
		return "", err
	}
	deployment := state.environmentBinding(policy.TemplateID, environment)
	if deployment == nil || deployment.status != DeploymentActive {
		h.mu.Unlock()
		return "", ErrAssistPolicyIneligible
	}
	release := deployment.request.Release
	idempotencyHash, err := hashJSON(struct {
		Org         string
		Workspace   string
		CaseID      string
		Policy      casedomain.AssistAutomation
		Source      AssistPolicySource
		Release     int
		Environment engine.Environment
	}{
		id.Org, id.Workspace, view.CaseID, policy, source, release, environment,
	})
	if err != nil {
		h.mu.Unlock()
		return "", err
	}
	for _, assist := range state.assists {
		if assist.request.IdempotencyHash == idempotencyHash {
			assistID := assist.request.AssistID
			h.mu.Unlock()
			return assistID, nil
		}
	}
	h.mu.Unlock()
	request := AssistRequested{
		CaseID: view.CaseID, Kind: AssistKind(policy.Kind),
		TemplateID: policy.TemplateID, Release: release, Environment: environment,
		EvidenceIDs: evidenceIDs, EvidenceSeq: evidenceSeq,
		IdempotencyHash: idempotencyHash, PolicySource: &source,
		Subject: view.Subject,
	}
	if strings.TrimSpace(request.Subject) == "" {
		request.Subject = "case/" + view.CaseID
	}
	if h.contentSealer != nil {
		input := AssistInput{
			CaseID: view.CaseID, CaseType: view.CaseType,
			Jurisdiction: view.Jurisdiction,
			Context:      append(json.RawMessage(nil), view.Context...),
			Evidence:     selected,
		}
		encoded, encodeErr := json.Marshal(input)
		if encodeErr != nil {
			return "", fmt.Errorf(
				"agent governance: encode policy assist input snapshot: %w", encodeErr,
			)
		}
		request.SealedInput, err = h.contentSealer.Seal(
			ctx, id, request.Subject, encoded,
		)
		if err != nil {
			return "", fmt.Errorf(
				"agent governance: seal policy assist input snapshot: %w", err,
			)
		}
		request.InputSubject = request.Subject
	}
	assistID, _, err := h.RequestAssist(ctx, id, request)
	return assistID, err
}

func policyEvidence(
	view cases.CaseView,
	policy casedomain.AssistAutomation,
) ([]cases.EvidenceLink, []string, uint64, error) {
	required := make(map[string]bool, len(policy.EvidenceRequirements))
	for _, requirement := range policy.EvidenceRequirements {
		required[requirement] = true
	}
	found := make(map[string]bool, len(required))
	selected := make([]cases.EvidenceLink, 0, len(view.Evidence))
	var evidenceSeq uint64
	for _, evidence := range view.Evidence {
		if !required[evidence.Requirement] {
			continue
		}
		selected = append(selected, evidence)
		found[evidence.Requirement] = true
		if evidence.LinkedSeq > evidenceSeq {
			evidenceSeq = evidence.LinkedSeq
		}
	}
	for requirement := range required {
		if !found[requirement] {
			return nil, nil, 0, fmt.Errorf(
				"%w: requirement %q", ErrAssistPolicyWaiting, requirement,
			)
		}
	}
	if evidenceSeq < 1 {
		return nil, nil, 0, errors.New(
			"agent governance: policy evidence has no authoritative sequence",
		)
	}
	sort.Slice(selected, func(i, j int) bool {
		return selected[i].EvidenceID < selected[j].EvidenceID
	})
	evidenceIDs := make([]string, len(selected))
	for index, evidence := range selected {
		evidenceIDs[index] = evidence.EvidenceID
	}
	return selected, evidenceIDs, evidenceSeq, nil
}
