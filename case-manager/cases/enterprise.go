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
	default:
		return nil
	}
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
	var payload events.QueueConfigured
	if err := decode(event, &payload); err != nil {
		return err
	}
	var definition domain.QueueDefinition
	if err := json.Unmarshal(payload.Definition, &definition); err != nil {
		return fmt.Errorf("cases: decode queue definition seq %d: %w", event.Seq, err)
	}
	if err := definition.Validate(); err != nil {
		return fmt.Errorf("cases: invalid queue definition seq %d: %w", event.Seq, err)
	}
	if payload.Key != definition.Key {
		return fmt.Errorf("cases: queue configuration seq %d key mismatch", event.Seq)
	}
	return store.PutDoc(ctx, st, QueuesCollection, store.Key(event.Org, event.Workspace, payload.Key), QueueView{
		Org: event.Org, Workspace: event.Workspace, Definition: definition,
		ConfiguredBy: event.Actor, ConfiguredAt: event.Time, Seq: event.Seq,
	})
}

func applyReviewerConfigured(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload events.ReviewerConfigured
	if err := decode(event, &payload); err != nil {
		return err
	}
	var profile domain.ReviewerProfile
	if err := json.Unmarshal(payload.Profile, &profile); err != nil {
		return fmt.Errorf("cases: decode reviewer profile seq %d: %w", event.Seq, err)
	}
	if err := profile.Validate(); err != nil {
		return fmt.Errorf("cases: invalid reviewer profile seq %d: %w", event.Seq, err)
	}
	if payload.Actor != profile.Actor {
		return fmt.Errorf("cases: reviewer configuration seq %d actor mismatch", event.Seq)
	}
	return store.PutDoc(ctx, st, ReviewersCollection, store.Key(event.Org, event.Workspace, payload.Actor), ReviewerView{
		Org: event.Org, Workspace: event.Workspace, Profile: profile,
		ConfiguredBy: event.Actor, ConfiguredAt: event.Time, Seq: event.Seq,
	})
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
		view.Status = state
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
			Label: payload.Label, ContentHash: payload.ContentHash,
		})
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
		})
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
	views, err := store.ListDocs[CaseTypeView](ctx, st, CaseTypeLatestCollection, store.Key(id.Org, id.Workspace, ""))
	if err != nil {
		return nil, err
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Definition.Name < views[j].Definition.Name })
	return views, nil
}

// CaseTypeVersion reads one immutable definition version.
func CaseTypeVersion(ctx context.Context, st store.Store, id identity.Identity, key string, version int) (CaseTypeView, bool, error) {
	return store.GetDoc[CaseTypeView](ctx, st, CaseTypesCollection, store.Key(id.Org, id.Workspace, key+":"+strconv.Itoa(version)))
}

// ListQueues lists active queues.
func ListQueues(ctx context.Context, st store.Store, id identity.Identity) ([]QueueView, error) {
	views, err := store.ListDocs[QueueView](ctx, st, QueuesCollection, store.Key(id.Org, id.Workspace, ""))
	if err != nil {
		return nil, err
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Definition.Key < views[j].Definition.Key })
	return views, nil
}

// ListReviewers lists active reviewer profiles.
func ListReviewers(ctx context.Context, st store.Store, id identity.Identity) ([]ReviewerView, error) {
	views, err := store.ListDocs[ReviewerView](ctx, st, ReviewersCollection, store.Key(id.Org, id.Workspace, ""))
	if err != nil {
		return nil, err
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Profile.Actor < views[j].Profile.Actor })
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
