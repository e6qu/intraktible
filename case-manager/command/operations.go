// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/case-manager/events"
	decisionevents "github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

type workState struct {
	exists              bool
	caseType            string
	caseTypeVersion     int
	status              domain.CaseStatus
	priority            domain.Priority
	assignee            string
	sourceDecisionID    string
	sourceSuspended     bool
	disposition         string
	reasonCode          string
	dispositionActor    string
	dispositionCount    int
	priorityCount       int
	evidence            map[string]events.CaseEvidenceLinked
	attachments         map[string]events.CaseAttachmentRegistered
	qa                  *events.CaseQASelected
	qaReviewed          bool
	pendingSecondReview bool
}

// UpdateFields applies a role-authorized patch under the exact pinned type
// version. Concurrent editors serialize on the context revision and revalidate
// their patch against the winner's resulting object.
func (h *Handler) UpdateFields(
	ctx context.Context,
	id identity.Identity,
	caseID string,
	fields json.RawMessage,
	role string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	var patch map[string]any
	if err := json.Unmarshal(fields, &patch); err != nil || patch == nil || len(patch) == 0 {
		return eventlog.Envelope{}, errors.New("case-manager: fields must be a non-empty JSON object")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; ; attempt++ {
		state, err := h.caseState(ctx, id, caseID)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		if state.caseTypeVersion == 0 {
			return eventlog.Envelope{}, errors.New("case-manager: editable fields require a versioned case type")
		}
		if state.terminal {
			return eventlog.Envelope{}, fmt.Errorf("case-manager: terminal case %q fields are immutable", caseID)
		}
		published, err := h.caseTypeAtVersion(ctx, id, state.caseType, state.caseTypeVersion)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		editable := map[string]bool{}
		for _, layout := range published.Definition.Layouts {
			if layout.Role != role {
				continue
			}
			for _, key := range layout.Editable {
				editable[key] = true
			}
		}
		for key := range patch {
			if !editable[key] {
				return eventlog.Envelope{}, fmt.Errorf("case-manager: role %q cannot edit field %q", role, key)
			}
		}
		merged, err := domain.PatchContext(state.context, fields)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		if err := published.Definition.ValidateContext(merged); err != nil {
			return eventlog.Envelope{}, err
		}
		payload, err := json.Marshal(events.CaseFieldsUpdated{CaseID: caseID, Fields: fields})
		if err != nil {
			return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal field update: %w", err)
		}
		event, err := h.appendUnique(
			ctx, id, events.TypeCaseFieldsUpdated, payload,
			contextClaim(caseID, state.contextCount),
		)
		if errors.Is(err, eventlog.ErrConflict) && attempt < maxClaimRetries {
			continue
		}
		return event, err
	}
}

// SetPriority changes one case's urgency under its pinned type contract.
func (h *Handler) SetPriority(ctx context.Context, id identity.Identity, caseID string, priority domain.Priority) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if !priority.Valid() {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: invalid priority %q", priority)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.workState(ctx, id, caseID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.caseTypeVersion > 0 {
		published, err := h.caseTypeAtVersion(ctx, id, state.caseType, state.caseTypeVersion)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		if !published.Definition.AllowsPriority(priority) {
			return eventlog.Envelope{}, fmt.Errorf("case-manager: type %q version %d does not permit priority %q", state.caseType, state.caseTypeVersion, priority)
		}
	}
	payload, err := json.Marshal(events.CasePriorityChanged{CaseID: caseID, Priority: string(priority)})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal priority: %w", err)
	}
	return h.appendUnique(ctx, id, events.TypeCasePriorityChanged, payload, priorityClaim(caseID, state.priorityCount))
}

// LinkEvidence adds an immutable typed evidence relationship exactly once.
func (h *Handler) LinkEvidence(ctx context.Context, id identity.Identity, link events.CaseEvidenceLinked) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(link.CaseID) == "" || strings.TrimSpace(link.EvidenceID) == "" ||
		strings.TrimSpace(link.Kind) == "" || strings.TrimSpace(link.SubjectType) == "" ||
		strings.TrimSpace(link.SubjectID) == "" || strings.TrimSpace(link.Label) == "" {
		return eventlog.Envelope{}, errors.New("case-manager: case_id, evidence_id, kind, subject_type, subject_id, and label are required")
	}
	if !domain.EvidenceKind(link.Kind).Linkable() || !domain.EvidenceKind(link.SubjectType).Linkable() {
		return eventlog.Envelope{}, errors.New("case-manager: evidence kind and subject_type must be decision, entity, agent_run, connector, case, or alert")
	}
	if link.ContentHash != "" && !validSHA256(link.ContentHash) {
		return eventlog.Envelope{}, errors.New("case-manager: evidence content_hash must be a lowercase SHA-256 hex digest")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.workState(ctx, id, link.CaseID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if _, exists := state.evidence[link.EvidenceID]; exists {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: evidence %q is already linked", link.EvidenceID)
	}
	if err := h.validateRequirement(ctx, id, state, link.Requirement, link.Kind); err != nil {
		return eventlog.Envelope{}, err
	}
	payload, err := json.Marshal(link)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal evidence: %w", err)
	}
	return h.appendUnique(ctx, id, events.TypeCaseEvidenceLinked, payload, evidenceClaim(link.CaseID, link.EvidenceID))
}

// RegisterAttachment records immutable artifact metadata; bytes remain behind the
// approved external storage reference.
func (h *Handler) RegisterAttachment(ctx context.Context, id identity.Identity, attachment events.CaseAttachmentRegistered) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(attachment.CaseID) == "" || strings.TrimSpace(attachment.AttachmentID) == "" ||
		strings.TrimSpace(attachment.Name) == "" || strings.TrimSpace(attachment.MediaType) == "" ||
		attachment.Size < 0 || !validSHA256(attachment.SHA256) || strings.TrimSpace(attachment.StorageRef) == "" {
		return eventlog.Envelope{}, errors.New("case-manager: attachment id, name, media_type, non-negative size, SHA-256, and storage_ref are required")
	}
	if attachment.Subject != "" && attachment.LawfulBasis == "" {
		return eventlog.Envelope{}, errors.New("case-manager: attachment lawful_basis is required when subject is set")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.workState(ctx, id, attachment.CaseID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if _, exists := state.attachments[attachment.AttachmentID]; exists {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: attachment %q is already registered", attachment.AttachmentID)
	}
	if err := h.validateRequirement(ctx, id, state, attachment.Requirement, "attachment"); err != nil {
		return eventlog.Envelope{}, err
	}
	payload, err := json.Marshal(attachment)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal attachment: %w", err)
	}
	return h.appendUnique(ctx, id, events.TypeCaseAttachmentRegistered, payload, attachmentClaim(attachment.CaseID, attachment.AttachmentID))
}

// AccessAttachment audits an artifact retrieval boundary.
func (h *Handler) AccessAttachment(ctx context.Context, id identity.Identity, caseID, attachmentID, purpose string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(purpose) == "" {
		return eventlog.Envelope{}, errors.New("case-manager: attachment access purpose is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.workState(ctx, id, caseID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if _, exists := state.attachments[attachmentID]; !exists {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: unknown attachment %q", attachmentID)
	}
	payload, err := json.Marshal(events.CaseAttachmentAccessed{CaseID: caseID, AttachmentID: attachmentID, Purpose: purpose})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal attachment access: %w", err)
	}
	return h.append(ctx, id, events.TypeCaseAttachmentAccessed, payload)
}

// RecordDisposition validates the reason and required evidence under the exact
// pinned definition before recording the outcome.
func (h *Handler) RecordDisposition(
	ctx context.Context,
	id identity.Identity,
	caseID, dispositionKey, reasonCode, note, role string,
	override bool,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.workState(ctx, id, caseID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.caseTypeVersion == 0 {
		return eventlog.Envelope{}, errors.New("case-manager: dispositions require a versioned case type")
	}
	if state.disposition != "" {
		return eventlog.Envelope{}, errors.New("case-manager: case already has a recorded disposition")
	}
	if state.sourceSuspended {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: case %q belongs to suspended decision %q — record its review outcome to resume it", caseID, state.sourceDecisionID)
	}
	published, err := h.caseTypeAtVersion(ctx, id, state.caseType, state.caseTypeVersion)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	disposition, found := published.Definition.FindDisposition(dispositionKey)
	if !found || !slices.Contains(disposition.ReasonCodes, reasonCode) {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: disposition %q or reason %q is not valid for type %q version %d", dispositionKey, reasonCode, state.caseType, state.caseTypeVersion)
	}
	if !published.Definition.CanTransition(state.status, domain.CaseStatus(disposition.TerminalState), role) {
		return eventlog.Envelope{}, fmt.Errorf(
			"case-manager: role %q cannot disposition case %q from %s to %s",
			role, caseID, state.status, disposition.TerminalState,
		)
	}
	for _, requirement := range published.Definition.Evidence {
		if requirement.Required && !requirementSatisfied(state, requirement.Key) {
			return eventlog.Envelope{}, fmt.Errorf("case-manager: required evidence %q is missing", requirement.Key)
		}
	}
	payload, err := json.Marshal(events.CaseDispositionRecorded{
		CaseID: caseID, Disposition: dispositionKey, ReasonCode: reasonCode, Note: note,
		State: disposition.TerminalState, Override: override,
		RequiresSecondReview: disposition.RequiresSecondReview,
	})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal disposition: %w", err)
	}
	return h.appendUnique(ctx, id, events.TypeCaseDispositionRecorded, payload, dispositionClaim(caseID, state.dispositionCount))
}

// SelectQA deterministically samples a disposed case and assigns an independent
// reviewer. selected=false emits no event.
func (h *Handler) SelectQA(ctx context.Context, id identity.Identity, caseID, sampleID, reviewer string, rateBPS int) (bool, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return false, eventlog.Envelope{}, err
	}
	if rateBPS < 1 || rateBPS > 10000 || strings.TrimSpace(sampleID) == "" || strings.TrimSpace(reviewer) == "" {
		return false, eventlog.Envelope{}, errors.New("case-manager: sample_id, reviewer, and rate_bps 1..10000 are required")
	}
	sum := sha256.Sum256([]byte(sampleID + "\x00" + caseID))
	bucket := int(sum[0])<<8 | int(sum[1])
	if bucket%10000 >= rateBPS {
		return false, eventlog.Envelope{}, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.workState(ctx, id, caseID)
	if err != nil {
		return false, eventlog.Envelope{}, err
	}
	if state.disposition == "" {
		return false, eventlog.Envelope{}, errors.New("case-manager: QA selection requires a recorded disposition")
	}
	if reviewer == state.dispositionActor {
		return false, eventlog.Envelope{}, errors.New("case-manager: QA reviewer must be independent of the primary reviewer")
	}
	payload, err := json.Marshal(events.CaseQASelected{
		CaseID: caseID, SampleID: sampleID, PrimaryActor: state.dispositionActor, Reviewer: reviewer,
	})
	if err != nil {
		return false, eventlog.Envelope{}, fmt.Errorf("case-manager: marshal QA selection: %w", err)
	}
	event, err := h.appendUnique(ctx, id, events.TypeCaseQASelected, payload, qaClaim(caseID, sampleID))
	return err == nil, event, err
}

// ReviewQA derives agreement from the primary outcome rather than trusting a
// caller-authored flag.
func (h *Handler) ReviewQA(ctx context.Context, id identity.Identity, caseID, sampleID, disposition, reasonCode, note string, override bool) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.workState(ctx, id, caseID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.qa == nil || state.qa.SampleID != sampleID || state.qa.Reviewer != id.Actor {
		return eventlog.Envelope{}, errors.New("case-manager: QA sample is not assigned to this reviewer")
	}
	if state.qaReviewed {
		return eventlog.Envelope{}, errors.New("case-manager: QA sample is already completed")
	}
	if state.caseTypeVersion == 0 {
		return eventlog.Envelope{}, errors.New("case-manager: QA review requires a versioned case type")
	}
	published, err := h.caseTypeAtVersion(ctx, id, state.caseType, state.caseTypeVersion)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	qaDisposition, found := published.Definition.FindDisposition(disposition)
	if !found || !slices.Contains(qaDisposition.ReasonCodes, reasonCode) {
		return eventlog.Envelope{}, fmt.Errorf(
			"case-manager: QA disposition %q or reason %q is not valid for type %q version %d",
			disposition, reasonCode, state.caseType, state.caseTypeVersion,
		)
	}
	agreement := disposition == state.disposition && reasonCode == state.reasonCode
	if agreement && override {
		return eventlog.Envelope{}, errors.New("case-manager: an agreeing QA review cannot override the primary outcome")
	}
	if state.pendingSecondReview && !agreement && !override {
		return eventlog.Envelope{}, errors.New("case-manager: required second review must agree or explicitly override")
	}
	effectiveState := ""
	if state.pendingSecondReview {
		if override {
			effectiveState = qaDisposition.TerminalState
		} else {
			primaryDisposition, _ := published.Definition.FindDisposition(state.disposition)
			effectiveState = primaryDisposition.TerminalState
		}
	}
	payload, err := json.Marshal(events.CaseQAReviewed{
		CaseID: caseID, SampleID: sampleID, Disposition: disposition, ReasonCode: reasonCode,
		Agreement: agreement,
		Override:  override, Note: note, State: effectiveState,
	})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal QA review: %w", err)
	}
	claim := qaReviewClaim(caseID, sampleID)
	if state.pendingSecondReview {
		claim = statusClaim(caseID, state.dispositionCount)
	}
	return h.appendUnique(ctx, id, events.TypeCaseQAReviewed, payload, claim)
}

// AddReviewerFeedback records reasoned feedback after a completed QA review.
func (h *Handler) AddReviewerFeedback(ctx context.Context, id identity.Identity, caseID, sampleID, text string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(text) == "" {
		return eventlog.Envelope{}, errors.New("case-manager: reviewer feedback text is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.workState(ctx, id, caseID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.qa == nil || state.qa.SampleID != sampleID || !state.qaReviewed {
		return eventlog.Envelope{}, errors.New("case-manager: feedback requires a completed QA sample")
	}
	payload, err := json.Marshal(events.CaseReviewerFeedbackAdded{
		CaseID: caseID, SampleID: sampleID, Reviewer: state.dispositionActor, Text: text,
	})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal reviewer feedback: %w", err)
	}
	return h.append(ctx, id, events.TypeCaseReviewerFeedbackAdded, payload)
}

// SaveView upserts one actor-owned reusable queue query.
func (h *Handler) SaveView(ctx context.Context, id identity.Identity, viewID, name string, query json.RawMessage) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if viewID == "" {
		viewID = h.newID()
	}
	if strings.TrimSpace(name) == "" {
		return "", eventlog.Envelope{}, errors.New("case-manager: saved view name is required")
	}
	var object map[string]any
	if len(query) == 0 {
		return "", eventlog.Envelope{}, errors.New("case-manager: saved view query must be a JSON object")
	}
	if err := json.Unmarshal(query, &object); err != nil {
		return "", eventlog.Envelope{}, fmt.Errorf("case-manager: saved view query must be a JSON object: %w", err)
	}
	if object == nil {
		return "", eventlog.Envelope{}, errors.New("case-manager: saved view query must be a JSON object")
	}
	payload, err := json.Marshal(events.CaseSavedViewUpserted{
		ViewID: viewID, Name: name, Owner: id.Actor, Query: query,
	})
	if err != nil {
		return "", eventlog.Envelope{}, fmt.Errorf("case-manager: marshal saved view: %w", err)
	}
	event, err := h.append(ctx, id, events.TypeCaseSavedViewUpserted, payload)
	return viewID, event, err
}

// DeleteView deletes one actor-owned saved view.
func (h *Handler) DeleteView(ctx context.Context, id identity.Identity, viewID string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(viewID) == "" {
		return eventlog.Envelope{}, errors.New("case-manager: saved view id is required")
	}
	payload, err := json.Marshal(events.CaseSavedViewDeleted{ViewID: viewID})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal saved view deletion: %w", err)
	}
	return h.append(ctx, id, events.TypeCaseSavedViewDeleted, payload)
}

func (h *Handler) validateRequirement(ctx context.Context, id identity.Identity, state workState, requirement, kind string) error {
	if requirement == "" || state.caseTypeVersion == 0 {
		return nil
	}
	published, err := h.caseTypeAtVersion(ctx, id, state.caseType, state.caseTypeVersion)
	if err != nil {
		return err
	}
	for _, configured := range published.Definition.Evidence {
		if configured.Key == requirement {
			if slices.Contains(configured.Kinds, kind) {
				return nil
			}
			return fmt.Errorf("case-manager: evidence requirement %q does not accept kind %q", requirement, kind)
		}
	}
	return fmt.Errorf("case-manager: unknown evidence requirement %q", requirement)
}

func (h *Handler) caseTypeAtVersion(ctx context.Context, id identity.Identity, key string, version int) (PublishedCaseType, error) {
	recorded, err := h.log.Read(ctx, 0)
	if err != nil {
		return PublishedCaseType{}, fmt.Errorf("case-manager: read case type version: %w", err)
	}
	for _, event := range recorded {
		if event.Org != id.Org || event.Workspace != id.Workspace || event.Type != events.TypeCaseTypePublished {
			continue
		}
		var payload events.CaseTypePublished
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return PublishedCaseType{}, fmt.Errorf("case-manager: decode case type seq %d: %w", event.Seq, err)
		}
		if payload.Key == key && payload.Version == version {
			var definition domain.CaseTypeDefinition
			if err := json.Unmarshal(payload.Definition, &definition); err != nil {
				return PublishedCaseType{}, fmt.Errorf("case-manager: decode case type definition seq %d: %w", event.Seq, err)
			}
			if err := definition.Validate(); err != nil {
				return PublishedCaseType{}, err
			}
			return PublishedCaseType{Version: version, Definition: definition}, nil
		}
	}
	return PublishedCaseType{}, fmt.Errorf("case-manager: missing case type %q version %d", key, version)
}

func (h *Handler) workState(ctx context.Context, id identity.Identity, caseID string) (workState, error) {
	recorded, err := h.log.Read(ctx, 0)
	if err != nil {
		return workState{}, fmt.Errorf("case-manager: read work state: %w", err)
	}
	state := workState{
		priority: domain.PriorityNormal, status: domain.StatusNeedsReview,
		evidence:    map[string]events.CaseEvidenceLinked{},
		attachments: map[string]events.CaseAttachmentRegistered{},
	}
	suspended := map[string]bool{}
	latestTypes := map[string]PublishedCaseType{}
	for _, event := range recorded {
		if event.Org != id.Org || event.Workspace != id.Workspace {
			continue
		}
		switch event.Type {
		case events.TypeCaseTypePublished:
			var payload events.CaseTypePublished
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return workState{}, fmt.Errorf("case-manager: decode case type seq %d: %w", event.Seq, err)
			}
			var definition domain.CaseTypeDefinition
			if err := json.Unmarshal(payload.Definition, &definition); err != nil {
				return workState{}, fmt.Errorf("case-manager: decode case type definition seq %d: %w", event.Seq, err)
			}
			latestTypes[payload.Key] = PublishedCaseType{Version: payload.Version, Definition: definition}
		case events.TypeReviewRequested:
			var payload events.ReviewRequested
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return workState{}, err
			}
			if payload.CaseID == caseID {
				state.exists, state.caseType, state.caseTypeVersion = true, payload.CaseType, payload.CaseTypeVersion
				if payload.InitialState != "" {
					state.status = domain.CaseStatus(payload.InitialState)
				}
				if payload.Priority != "" {
					state.priority = domain.Priority(payload.Priority)
				}
				state.sourceDecisionID = payload.SourceDecisionID
			}
		case decisionevents.TypeManualReviewRequested:
			var payload decisionevents.ManualReviewRequested
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return workState{}, err
			}
			if payload.CaseID == caseID {
				state.exists, state.caseType, state.sourceDecisionID = true, payload.CaseType, payload.DecisionID
				if published, governed := latestTypes[payload.CaseType]; governed {
					state.caseTypeVersion = published.Version
					state.status = published.Definition.InitialState
					if !published.Definition.AllowsPriority(state.priority) {
						state.priority = published.Definition.Priorities[0]
					}
				}
				state.sourceSuspended = suspended[payload.DecisionID]
			}
		case decisionevents.TypeDecisionSuspended:
			var payload decisionevents.DecisionSuspended
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return workState{}, err
			}
			suspended[payload.DecisionID] = true
			if payload.CaseID == caseID {
				state.sourceSuspended = true
			}
		case decisionevents.TypeDecisionResumed:
			var payload decisionevents.DecisionResumed
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return workState{}, err
			}
			if payload.CaseID == caseID {
				state.sourceSuspended = false
				state.status = domain.StatusCompleted
			}
		case events.TypeCaseAssigned:
			var payload events.CaseAssigned
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return workState{}, fmt.Errorf("case-manager: decode assignment seq %d: %w", event.Seq, err)
			}
			if payload.CaseID == caseID {
				state.assignee = payload.Assignee
			}
		case events.TypeCaseRouted:
			var payload events.CaseRouted
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return workState{}, fmt.Errorf("case-manager: decode route seq %d: %w", event.Seq, err)
			}
			if payload.CaseID == caseID && payload.Assignee != "" {
				state.assignee = payload.Assignee
			}
		case events.TypeCaseStatusChanged:
			var payload events.CaseStatusChanged
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return workState{}, fmt.Errorf("case-manager: decode status seq %d: %w", event.Seq, err)
			}
			if payload.CaseID == caseID {
				state.status = domain.CaseStatus(payload.Status)
				state.dispositionCount++
			}
		case events.TypeCasePriorityChanged:
			var payload events.CasePriorityChanged
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return workState{}, fmt.Errorf("case-manager: decode priority seq %d: %w", event.Seq, err)
			}
			if payload.CaseID == caseID {
				state.priority, state.priorityCount = domain.Priority(payload.Priority), state.priorityCount+1
			}
		case events.TypeCaseDispositionRecorded:
			var payload events.CaseDispositionRecorded
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return workState{}, fmt.Errorf("case-manager: decode disposition seq %d: %w", event.Seq, err)
			}
			if payload.CaseID == caseID {
				state.disposition, state.reasonCode, state.dispositionActor = payload.Disposition, payload.ReasonCode, event.Actor
				state.pendingSecondReview = payload.RequiresSecondReview
				if !payload.RequiresSecondReview {
					state.status = domain.CaseStatus(payload.State)
				}
				state.dispositionCount++
			}
		case events.TypeCaseSLABreached:
			var payload events.CaseSLABreached
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return workState{}, fmt.Errorf("case-manager: decode SLA breach seq %d: %w", event.Seq, err)
			}
			if payload.CaseID == caseID {
				state.dispositionCount++
			}
		case events.TypeCaseSLAReminder:
			var payload events.CaseSLAReminder
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return workState{}, fmt.Errorf("case-manager: decode SLA reminder seq %d: %w", event.Seq, err)
			}
			if payload.CaseID == caseID {
				state.dispositionCount++
			}
		case events.TypeCaseEvidenceLinked:
			var payload events.CaseEvidenceLinked
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return workState{}, fmt.Errorf("case-manager: decode evidence seq %d: %w", event.Seq, err)
			}
			if payload.CaseID == caseID {
				state.evidence[payload.EvidenceID] = payload
			}
		case events.TypeCaseAttachmentRegistered:
			var payload events.CaseAttachmentRegistered
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return workState{}, fmt.Errorf("case-manager: decode attachment seq %d: %w", event.Seq, err)
			}
			if payload.CaseID == caseID {
				state.attachments[payload.AttachmentID] = payload
			}
		case events.TypeCaseQASelected:
			var payload events.CaseQASelected
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return workState{}, fmt.Errorf("case-manager: decode QA selection seq %d: %w", event.Seq, err)
			}
			if payload.CaseID == caseID {
				state.qa = &payload
			}
		case events.TypeCaseQAReviewed:
			var payload events.CaseQAReviewed
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return workState{}, fmt.Errorf("case-manager: decode QA review seq %d: %w", event.Seq, err)
			}
			if payload.CaseID == caseID {
				state.qaReviewed = true
				if payload.State != "" {
					state.status = domain.CaseStatus(payload.State)
					state.pendingSecondReview = false
					state.dispositionCount++
				}
			}
		}
	}
	if !state.exists {
		return workState{}, fmt.Errorf("case-manager: unknown case %q", caseID)
	}
	return state, nil
}

func requirementSatisfied(state workState, requirement string) bool {
	for _, link := range state.evidence {
		if link.Requirement == requirement {
			return true
		}
	}
	for _, attachment := range state.attachments {
		if attachment.Requirement == requirement {
			return true
		}
	}
	return false
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func priorityClaim(caseID string, count int) string {
	return "case.priority\x00" + caseID + "\x00" + strconv.Itoa(count)
}
func contextClaim(caseID string, count int) string {
	return "case.context\x00" + caseID + "\x00" + strconv.Itoa(count)
}
func dispositionClaim(caseID string, count int) string {
	return statusClaim(caseID, count)
}
func evidenceClaim(caseID, evidenceID string) string {
	return "case.evidence\x00" + caseID + "\x00" + evidenceID
}
func attachmentClaim(caseID, attachmentID string) string {
	return "case.attachment\x00" + caseID + "\x00" + attachmentID
}
func qaClaim(caseID, sampleID string) string {
	return "case.qa\x00" + caseID + "\x00" + sampleID
}
func qaReviewClaim(caseID, sampleID string) string {
	return "case.qa_review\x00" + caseID + "\x00" + sampleID
}
