// SPDX-License-Identifier: AGPL-3.0-or-later

package cases

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/case-manager/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

const (
	// CaseTypesCollection holds every immutable definition version.
	CaseTypesCollection = "case_types"
	// CaseTypeLatestCollection holds the active version per type key.
	CaseTypeLatestCollection = "case_type_latest"
	// QueuesCollection holds active routing queue definitions.
	QueuesCollection = "case_queues"
	// ReviewersCollection holds active reviewer routing profiles.
	ReviewersCollection = "case_reviewers"
	// SavedViewsCollection holds per-actor saved queue queries.
	SavedViewsCollection = "case_saved_views"
	// BulkCollection holds authoritative bounded bulk-operation manifests.
	BulkCollection = "case_bulk_operations"
)

// CaseTypeView is one immutable published definition.
type CaseTypeView struct {
	Org         string                    `json:"org"`
	Workspace   string                    `json:"workspace"`
	Key         string                    `json:"key"`
	Version     int                       `json:"version"`
	Definition  domain.CaseTypeDefinition `json:"definition"`
	PublishedBy string                    `json:"published_by"`
	PublishedAt time.Time                 `json:"published_at"`
	Seq         uint64                    `json:"seq"`
}

// QueueView is the active queue definition.
type QueueView struct {
	Org          string                 `json:"org"`
	Workspace    string                 `json:"workspace"`
	Definition   domain.QueueDefinition `json:"definition"`
	ConfiguredBy string                 `json:"configured_by"`
	ConfiguredAt time.Time              `json:"configured_at"`
	Seq          uint64                 `json:"seq"`
}

// ReviewerView is the active reviewer routing profile.
type ReviewerView struct {
	Org          string                 `json:"org"`
	Workspace    string                 `json:"workspace"`
	Profile      domain.ReviewerProfile `json:"profile"`
	ConfiguredBy string                 `json:"configured_by"`
	ConfiguredAt time.Time              `json:"configured_at"`
	Seq          uint64                 `json:"seq"`
}

// SavedView is an actor-owned reusable query.
type SavedView struct {
	Org       string          `json:"org"`
	Workspace string          `json:"workspace"`
	ViewID    string          `json:"view_id"`
	Name      string          `json:"name"`
	Owner     string          `json:"owner"`
	Query     json.RawMessage `json:"query"`
	UpdatedAt time.Time       `json:"updated_at"`
	Seq       uint64          `json:"seq"`
}

// BulkItemView is one logical case result in a bulk manifest.
type BulkItemView struct {
	CaseID  string `json:"case_id"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// BulkView is the replayable server-owned bulk-operation manifest.
type BulkView struct {
	Org            string         `json:"org"`
	Workspace      string         `json:"workspace"`
	BatchID        string         `json:"batch_id"`
	IdempotencyKey string         `json:"idempotency_key"`
	RequestHash    string         `json:"request_hash"`
	Operation      string         `json:"operation"`
	CaseIDs        []string       `json:"case_ids"`
	Status         string         `json:"status"`
	Succeeded      int            `json:"succeeded"`
	Failed         int            `json:"failed"`
	Items          []BulkItemView `json:"items"`
	StartedBy      string         `json:"started_by"`
	StartedAt      time.Time      `json:"started_at"`
	CompletedAt    time.Time      `json:"completed_at,omitempty"`
	Seq            uint64         `json:"seq"`
}

func applyEnterpriseEvent(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	switch event.Type {
	case events.TypeCaseTypePublished:
		return applyCaseTypePublished(ctx, event, st)
	case events.TypeQueueConfigured:
		return applyQueueConfigured(ctx, event, st)
	case events.TypeReviewerConfigured:
		return applyReviewerConfigured(ctx, event, st)
	case events.TypeCaseRouted:
		return applyRouted(ctx, event, st)
	case events.TypeCasePriorityChanged:
		return applyPriority(ctx, event, st)
	case events.TypeCaseFieldsUpdated:
		return applyFieldsUpdated(ctx, event, st)
	case events.TypeCaseDispositionRecorded:
		return applyDisposition(ctx, event, st)
	case events.TypeCaseEvidenceLinked:
		return applyEvidence(ctx, event, st)
	case events.TypeCaseAttachmentRegistered:
		return applyAttachment(ctx, event, st)
	case events.TypeCaseAttachmentAccessed:
		return applyAttachmentAccessed(ctx, event, st)
	case events.TypeCaseSavedViewUpserted:
		return applySavedView(ctx, event, st)
	case events.TypeCaseSavedViewDeleted:
		return applySavedViewDeleted(ctx, event, st)
	case events.TypeCaseQASelected:
		return applyQASelected(ctx, event, st)
	case events.TypeCaseQAReviewed:
		return applyQAReviewed(ctx, event, st)
	case events.TypeCaseReviewerFeedbackAdded:
		return applyReviewerFeedback(ctx, event, st)
	case events.TypeCaseBulkStarted:
		return applyBulkStarted(ctx, event, st)
	case events.TypeCaseBulkItemRecorded:
		return applyBulkItem(ctx, event, st)
	case events.TypeCaseBulkCompleted:
		return applyBulkCompleted(ctx, event, st)
	default:
		return nil
	}
}

func applyFieldsUpdated(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.CaseFieldsUpdated
	if err := decode(event, &payload); err != nil {
		return err
	}
	return updateChecked(ctx, st, event, payload.CaseID, func(view *CaseView) error {
		merged, err := domain.PatchContext(view.Context, payload.Fields)
		if err != nil {
			return fmt.Errorf("cases: apply field update seq %d: %w", event.Seq, err)
		}
		var keys map[string]any
		if err := json.Unmarshal(payload.Fields, &keys); err != nil {
			return fmt.Errorf("cases: decode field update keys seq %d: %w", event.Seq, err)
		}
		id := identity.Identity{Org: event.Org, Workspace: event.Workspace, Actor: event.Actor}
		published, found, err := CaseTypeVersion(
			ctx, st, id, view.CaseType, view.CaseTypeVersion,
		)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf(
				"cases: field update seq %d is missing case type %q version %d",
				event.Seq, view.CaseType, view.CaseTypeVersion,
			)
		}
		defined := map[string]bool{}
		for _, field := range published.Definition.Fields {
			defined[field.Key] = true
		}
		for key := range keys {
			if !defined[key] {
				return fmt.Errorf("cases: field update seq %d names unknown field %q", event.Seq, key)
			}
		}
		if err := published.Definition.ValidateContext(merged); err != nil {
			return fmt.Errorf("cases: field update seq %d violates pinned definition: %w", event.Seq, err)
		}
		view.Context = merged
		markAction(view, event.Time)
		names := make([]string, 0, len(keys))
		for key := range keys {
			names = append(names, key)
		}
		sort.Strings(names)
		view.Audit = append(view.Audit, audit(event, "fields_updated", strings.Join(names, ", ")))
		return nil
	})
}

func applyBulkStarted(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.CaseBulkStarted
	if err := decode(event, &payload); err != nil {
		return err
	}
	if payload.BatchID == "" || payload.IdempotencyKey == "" || payload.RequestHash == "" || len(payload.CaseIDs) == 0 {
		return fmt.Errorf("cases: bulk start seq %d is incomplete", event.Seq)
	}
	return store.PutDoc(ctx, st, BulkCollection, store.Key(event.Org, event.Workspace, payload.BatchID), BulkView{
		Org: event.Org, Workspace: event.Workspace, BatchID: payload.BatchID,
		IdempotencyKey: payload.IdempotencyKey, RequestHash: payload.RequestHash,
		Operation: payload.Operation, CaseIDs: payload.CaseIDs, Status: "running",
		Items: []BulkItemView{}, StartedBy: event.Actor, StartedAt: event.Time, Seq: event.Seq,
	})
}

func applyBulkItem(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.CaseBulkItemRecorded
	if err := decode(event, &payload); err != nil {
		return err
	}
	key := store.Key(event.Org, event.Workspace, payload.BatchID)
	view, found, err := store.GetDoc[BulkView](ctx, st, BulkCollection, key)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("cases: bulk item seq %d references unknown batch %q", event.Seq, payload.BatchID)
	}
	if view.Status != "running" {
		return fmt.Errorf("cases: bulk item seq %d arrived after batch completion", event.Seq)
	}
	for _, item := range view.Items {
		if item.CaseID == payload.CaseID {
			return fmt.Errorf("cases: duplicate bulk result for case %q", payload.CaseID)
		}
	}
	view.Items = append(view.Items, BulkItemView{CaseID: payload.CaseID, Success: payload.Success, Error: payload.Error})
	if payload.Success {
		view.Succeeded++
	} else {
		view.Failed++
	}
	view.Seq = event.Seq
	return store.PutDoc(ctx, st, BulkCollection, key, view)
}

func applyBulkCompleted(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.CaseBulkCompleted
	if err := decode(event, &payload); err != nil {
		return err
	}
	key := store.Key(event.Org, event.Workspace, payload.BatchID)
	view, found, err := store.GetDoc[BulkView](ctx, st, BulkCollection, key)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("cases: bulk completion seq %d references unknown batch %q", event.Seq, payload.BatchID)
	}
	if len(view.Items) != len(view.CaseIDs) || payload.Succeeded != view.Succeeded || payload.Failed != view.Failed {
		return fmt.Errorf("cases: bulk completion seq %d does not match item manifest", event.Seq)
	}
	view.Status, view.CompletedAt, view.Seq = "completed", event.Time, event.Seq
	return store.PutDoc(ctx, st, BulkCollection, key, view)
}

func applyCaseTypePublished(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.CaseTypePublished
	if err := decode(event, &payload); err != nil {
		return err
	}
	if payload.Version < 1 {
		return fmt.Errorf("cases: type publication seq %d has invalid version %d", event.Seq, payload.Version)
	}
	var definition domain.CaseTypeDefinition
	if err := json.Unmarshal(payload.Definition, &definition); err != nil {
		return fmt.Errorf("cases: decode type definition seq %d: %w", event.Seq, err)
	}
	if err := definition.Validate(); err != nil {
		return fmt.Errorf("cases: invalid type definition seq %d: %w", event.Seq, err)
	}
	if definition.Key != payload.Key {
		return fmt.Errorf("cases: type publication seq %d key mismatch %q != %q", event.Seq, payload.Key, definition.Key)
	}
	view := CaseTypeView{
		Org: event.Org, Workspace: event.Workspace, Key: payload.Key, Version: payload.Version,
		Definition: definition, PublishedBy: event.Actor, PublishedAt: event.Time, Seq: event.Seq,
	}
	versionKey := store.Key(event.Org, event.Workspace, payload.Key+":"+strconv.Itoa(payload.Version))
	if err := store.PutDoc(ctx, st, CaseTypesCollection, versionKey, view); err != nil {
		return err
	}
	latestKey := store.Key(event.Org, event.Workspace, payload.Key)
	latest, exists, err := store.GetDoc[CaseTypeView](ctx, st, CaseTypeLatestCollection, latestKey)
	if err != nil {
		return err
	}
	if exists && latest.Version >= payload.Version {
		return fmt.Errorf("cases: non-monotonic type version %q v%d after v%d", payload.Key, payload.Version, latest.Version)
	}
	return store.PutDoc(ctx, st, CaseTypeLatestCollection, latestKey, view)
}

func applyQueueConfigured(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	return applyConfiguration(
		ctx, event, st, "queue definition", QueuesCollection,
		queueConfigurationParts, queueConfigurationView,
	)
}

func applyReviewerConfigured(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	return applyConfiguration(
		ctx, event, st, "reviewer profile", ReviewersCollection,
		reviewerConfigurationParts, reviewerConfigurationView,
	)
}

func queueConfigurationParts(payload events.QueueConfigured) (json.RawMessage, string) {
	return payload.Definition, payload.Key
}

func queueConfigurationView(event eventlog.Envelope, definition domain.QueueDefinition) (string, QueueView) {
	return definition.Key, QueueView{
		Org: event.Org, Workspace: event.Workspace, Definition: definition,
		ConfiguredBy: event.Actor, ConfiguredAt: event.Time, Seq: event.Seq,
	}
}

func reviewerConfigurationParts(payload events.ReviewerConfigured) (json.RawMessage, string) {
	return payload.Profile, payload.Actor
}

func reviewerConfigurationView(event eventlog.Envelope, profile domain.ReviewerProfile) (string, ReviewerView) {
	return profile.Actor, ReviewerView{
		Org: event.Org, Workspace: event.Workspace, Profile: profile,
		ConfiguredBy: event.Actor, ConfiguredAt: event.Time, Seq: event.Seq,
	}
}

type configuration interface {
	Validate() error
}

func decodeConfiguration[T configuration](raw json.RawMessage, seq uint64, label string) (T, error) {
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, fmt.Errorf("cases: decode %s seq %d: %w", label, seq, err)
	}
	if err := value.Validate(); err != nil {
		return value, fmt.Errorf("cases: invalid %s seq %d: %w", label, seq, err)
	}
	return value, nil
}

func applyConfiguration[P any, T configuration, V any](
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
	label, collection string,
	unpack func(P) (json.RawMessage, string),
	build func(eventlog.Envelope, T) (string, V),
) error {
	var payload P
	if err := decode(event, &payload); err != nil {
		return err
	}
	raw, recordedKey := unpack(payload)
	value, err := decodeConfiguration[T](raw, event.Seq, label)
	if err != nil {
		return err
	}
	definedKey, view := build(event, value)
	if recordedKey != definedKey {
		return fmt.Errorf("cases: %s seq %d identity mismatch", label, event.Seq)
	}
	return store.PutDoc(ctx, st, collection, store.Key(event.Org, event.Workspace, recordedKey), view)
}

func applyRouted(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.CaseRouted
	if err := decode(event, &payload); err != nil {
		return err
	}
	if strings.TrimSpace(payload.Queue) == "" || strings.TrimSpace(payload.Explanation) == "" {
		return fmt.Errorf("cases: routed seq %d lacks queue or explanation", event.Seq)
	}
	return update(ctx, st, event, payload.CaseID, func(view *CaseView) {
		view.Queue = payload.Queue
		view.RoutingExplanation = payload.Explanation
		if payload.Assignee != "" {
			view.Assignee = payload.Assignee
		}
		markAction(view, event.Time)
		view.Audit = append(view.Audit, audit(event, "routed", payload.Explanation))
	})
}

func applyPriority(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.CasePriorityChanged
	if err := decode(event, &payload); err != nil {
		return err
	}
	priority := domain.Priority(payload.Priority)
	if !priority.Valid() {
		return fmt.Errorf("cases: priority seq %d has invalid value %q", event.Seq, payload.Priority)
	}
	return update(ctx, st, event, payload.CaseID, func(view *CaseView) {
		view.Priority = priority
		markAction(view, event.Time)
		view.Audit = append(view.Audit, audit(event, "priority_changed", "priority → "+payload.Priority))
	})
}

func applyDisposition(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.CaseDispositionRecorded
	if err := decode(event, &payload); err != nil {
		return err
	}
	state, valid := domain.ParseStateKey(payload.State)
	if !valid {
		return fmt.Errorf("cases: disposition seq %d has invalid state %q", event.Seq, payload.State)
	}
	return update(ctx, st, event, payload.CaseID, func(view *CaseView) {
		view.Disposition = payload.Disposition
		view.ReasonCode = payload.ReasonCode
		view.DispositionNote = payload.Note
		view.DispositionOverride = payload.Override
		markAction(view, event.Time)
		if !payload.RequiresSecondReview {
			view.Status = state
			view.ResolvedAt = event.Time
		}
		view.Audit = append(view.Audit, audit(event, "disposition_recorded", payload.Disposition+" / "+payload.ReasonCode))
	})
}

func applyEvidence(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.CaseEvidenceLinked
	if err := decode(event, &payload); err != nil {
		return err
	}
	return updateChecked(ctx, st, event, payload.CaseID, func(view *CaseView) error {
		for _, link := range view.Evidence {
			if link.EvidenceID == payload.EvidenceID {
				return fmt.Errorf("cases: evidence %q is already linked", payload.EvidenceID)
			}
		}
		view.Evidence = append(view.Evidence, EvidenceLink{
			EvidenceID: payload.EvidenceID, Requirement: payload.Requirement, Kind: payload.Kind,
			SubjectType: payload.SubjectType, SubjectID: payload.SubjectID,
			Label: payload.Label, ContentHash: payload.ContentHash, LinkedSeq: event.Seq,
		})
		markAction(view, event.Time)
		view.Audit = append(view.Audit, audit(event, "evidence_linked", payload.Kind+" → "+payload.SubjectType+"/"+payload.SubjectID))
		return nil
	})
}

func applyAttachment(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.CaseAttachmentRegistered
	if err := decode(event, &payload); err != nil {
		return err
	}
	return updateChecked(ctx, st, event, payload.CaseID, func(view *CaseView) error {
		for _, attachment := range view.Attachments {
			if attachment.AttachmentID == payload.AttachmentID {
				return fmt.Errorf("cases: attachment %q is already registered", payload.AttachmentID)
			}
		}
		view.Attachments = append(view.Attachments, Attachment{
			AttachmentID: payload.AttachmentID, Name: payload.Name, MediaType: payload.MediaType,
			Size: payload.Size, SHA256: payload.SHA256, StorageRef: payload.StorageRef,
			Requirement: payload.Requirement, Subject: payload.Subject, LawfulBasis: payload.LawfulBasis,
			RetainUntil: payload.RetainUntil, LegalHold: payload.LegalHold,
			RegisteredBy: event.Actor, RegisteredAt: event.Time,
			RegisteredSeq: event.Seq,
		})
		markAction(view, event.Time)
		view.Audit = append(view.Audit, audit(event, "attachment_registered", payload.Name+" sha256:"+payload.SHA256))
		return nil
	})
}

func applyAttachmentAccessed(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.CaseAttachmentAccessed
	if err := decode(event, &payload); err != nil {
		return err
	}
	return updateChecked(ctx, st, event, payload.CaseID, func(view *CaseView) error {
		for index := range view.Attachments {
			if view.Attachments[index].AttachmentID == payload.AttachmentID {
				view.Attachments[index].AccessCount++
				view.Attachments[index].LastAccessed = event.Time
				view.Audit = append(view.Audit, audit(event, "attachment_accessed", payload.AttachmentID+" / "+payload.Purpose))
				return nil
			}
		}
		return fmt.Errorf("cases: attachment access references unknown attachment %q", payload.AttachmentID)
	})
}

func applySavedView(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.CaseSavedViewUpserted
	if err := decode(event, &payload); err != nil {
		return err
	}
	if payload.Owner != event.Actor {
		return fmt.Errorf("cases: saved view seq %d owner differs from actor", event.Seq)
	}
	return store.PutDoc(ctx, st, SavedViewsCollection, store.Key(event.Org, event.Workspace, payload.Owner+":"+payload.ViewID), SavedView{
		Org: event.Org, Workspace: event.Workspace, ViewID: payload.ViewID,
		Name: payload.Name, Owner: payload.Owner, Query: payload.Query, UpdatedAt: event.Time, Seq: event.Seq,
	})
}

func applySavedViewDeleted(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.CaseSavedViewDeleted
	if err := decode(event, &payload); err != nil {
		return err
	}
	return st.Delete(ctx, SavedViewsCollection, store.Key(event.Org, event.Workspace, event.Actor+":"+payload.ViewID))
}

func applyQASelected(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.CaseQASelected
	if err := decode(event, &payload); err != nil {
		return err
	}
	return updateChecked(ctx, st, event, payload.CaseID, func(view *CaseView) error {
		if view.QA != nil {
			return fmt.Errorf("cases: case already belongs to QA sample %q", view.QA.SampleID)
		}
		view.QA = &QAReview{
			SampleID: payload.SampleID, PrimaryActor: payload.PrimaryActor,
			Reviewer: payload.Reviewer, Status: "pending",
		}
		markAction(view, event.Time)
		view.Audit = append(view.Audit, audit(event, "qa_selected", "sample "+payload.SampleID+" → "+payload.Reviewer))
		return nil
	})
}

func applyQAReviewed(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.CaseQAReviewed
	if err := decode(event, &payload); err != nil {
		return err
	}
	return updateChecked(ctx, st, event, payload.CaseID, func(view *CaseView) error {
		if view.QA == nil || view.QA.SampleID != payload.SampleID {
			return fmt.Errorf("cases: QA review references unknown sample %q", payload.SampleID)
		}
		if view.QA.Status == "completed" {
			return fmt.Errorf("cases: QA sample %q is already completed", payload.SampleID)
		}
		view.QA.Status = "completed"
		view.QA.Disposition = payload.Disposition
		view.QA.ReasonCode = payload.ReasonCode
		view.QA.Agreement = payload.Agreement
		view.QA.Override = payload.Override
		view.QA.Note = payload.Note
		view.QA.Validated = payload.Agreement || payload.Override
		view.QA.Disputed = !payload.Agreement && !payload.Override
		if payload.Override {
			view.QA.Effective = payload.Disposition
			view.QA.EffectiveReason = payload.ReasonCode
		} else if payload.Agreement {
			view.QA.Effective = view.Disposition
			view.QA.EffectiveReason = view.ReasonCode
		}
		if payload.State != "" {
			state, valid := domain.ParseStateKey(payload.State)
			if !valid {
				return fmt.Errorf("cases: QA review has invalid terminal state %q", payload.State)
			}
			view.Status = state
			view.ResolvedAt = event.Time
		}
		view.Audit = append(view.Audit, audit(event, "qa_reviewed", payload.Disposition+" / "+payload.ReasonCode))
		return nil
	})
}

func applyReviewerFeedback(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.CaseReviewerFeedbackAdded
	if err := decode(event, &payload); err != nil {
		return err
	}
	return updateChecked(ctx, st, event, payload.CaseID, func(view *CaseView) error {
		if view.QA == nil || view.QA.SampleID != payload.SampleID {
			return fmt.Errorf("cases: reviewer feedback references unknown QA sample %q", payload.SampleID)
		}
		if view.QA.Status != "completed" {
			return fmt.Errorf("cases: reviewer feedback requires a completed QA sample")
		}
		view.QA.Feedback = payload.Text
		view.QA.FeedbackAuthor = event.Actor
		view.Audit = append(view.Audit, audit(event, "reviewer_feedback_added", "feedback for "+payload.Reviewer))
		return nil
	})
}

func updateChecked(
	ctx context.Context,
	st store.Store,
	event eventlog.Envelope,
	caseID string,
	mutate func(*CaseView) error,
) error {
	key := store.Key(event.Org, event.Workspace, caseID)
	view, found, err := store.GetDoc[CaseView](ctx, st, Collection, key)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("cases: event seq %d for unknown case %q", event.Seq, caseID)
	}
	if err := mutate(&view); err != nil {
		return fmt.Errorf("cases: apply %s seq %d: %w", event.Type, event.Seq, err)
	}
	view.UpdatedAt = event.Time
	return store.PutDoc(ctx, st, Collection, key, view)
}

// LatestCaseTypes lists active case-type versions.
func LatestCaseTypes(ctx context.Context, st store.Store, id identity.Identity) ([]CaseTypeView, error) {
	return listTenantDocs(ctx, st, id, CaseTypeLatestCollection,
		func(a, b CaseTypeView) bool { return a.Definition.Name < b.Definition.Name })
}

// CaseTypeVersion reads one immutable definition version.
func CaseTypeVersion(ctx context.Context, st store.Store, id identity.Identity, key string, version int) (CaseTypeView, bool, error) {
	return store.GetDoc[CaseTypeView](ctx, st, CaseTypesCollection, store.Key(id.Org, id.Workspace, key+":"+strconv.Itoa(version)))
}

// ListQueues lists active queues.
func ListQueues(ctx context.Context, st store.Store, id identity.Identity) ([]QueueView, error) {
	return listTenantDocs(ctx, st, id, QueuesCollection,
		func(a, b QueueView) bool { return a.Definition.Key < b.Definition.Key })
}

// ListReviewers lists active reviewer profiles.
func ListReviewers(ctx context.Context, st store.Store, id identity.Identity) ([]ReviewerView, error) {
	return listTenantDocs(ctx, st, id, ReviewersCollection,
		func(a, b ReviewerView) bool { return a.Profile.Actor < b.Profile.Actor })
}

func listTenantDocs[T any](
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	collection string,
	less func(T, T) bool,
) ([]T, error) {
	views, err := store.ListDocs[T](ctx, st, collection, store.Key(id.Org, id.Workspace, ""))
	if err != nil {
		return nil, err
	}
	sort.Slice(views, func(i, j int) bool { return less(views[i], views[j]) })
	return views, nil
}

// ListSavedViews lists the caller's saved views.
func ListSavedViews(ctx context.Context, st store.Store, id identity.Identity) ([]SavedView, error) {
	views, err := store.ListDocs[SavedView](ctx, st, SavedViewsCollection, store.Key(id.Org, id.Workspace, id.Actor+":"))
	if err != nil {
		return nil, err
	}
	slices.SortFunc(views, func(a, b SavedView) int { return strings.Compare(a.Name, b.Name) })
	return views, nil
}

// ReadBulk reads one tenant-scoped bulk manifest.
func ReadBulk(ctx context.Context, st store.Store, id identity.Identity, batchID string) (BulkView, bool, error) {
	return store.GetDoc[BulkView](ctx, st, BulkCollection, store.Key(id.Org, id.Workspace, batchID))
}

// ListBulk lists recent bulk manifests.
func ListBulk(ctx context.Context, st store.Store, id identity.Identity) ([]BulkView, error) {
	views, err := store.ListDocs[BulkView](ctx, st, BulkCollection, store.Key(id.Org, id.Workspace, ""))
	if err != nil {
		return nil, err
	}
	slices.SortFunc(views, func(a, b BulkView) int {
		if a.StartedAt.Equal(b.StartedAt) {
			return strings.Compare(b.BatchID, a.BatchID)
		}
		if a.StartedAt.After(b.StartedAt) {
			return -1
		}
		return 1
	})
	return views, nil
}
