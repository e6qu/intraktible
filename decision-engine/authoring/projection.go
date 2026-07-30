// SPDX-License-Identifier: AGPL-3.0-or-later

package authoring

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

const (
	DraftCollection             = "decision_authoring_drafts"
	DraftRevisionCollection     = "decision_authoring_draft_revisions"
	ComponentCollection         = "decision_authoring_components"
	ChangeSetCollection         = "decision_authoring_changesets"
	ComponentConsumerCollection = "decision_authoring_component_consumers"
	PresenceCollection          = "decision_authoring_presence"
)

// DraftView is the authoritative current full-snapshot draft revision.
type DraftView struct {
	Org           string          `json:"org"`
	Workspace     string          `json:"workspace"`
	DraftID       string          `json:"draft_id"`
	FlowID        string          `json:"flow_id"`
	BaseVersion   int             `json:"base_version"`
	Revision      int             `json:"revision"`
	State         DraftState      `json:"state"`
	Title         string          `json:"title"`
	Graph         events.Graph    `json:"graph"`
	InputSchema   json.RawMessage `json:"input_schema,omitempty"`
	CreatedBy     string          `json:"created_by"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedBy     string          `json:"updated_by"`
	UpdatedAt     time.Time       `json:"updated_at"`
	ArchivedBy    string          `json:"archived_by,omitempty"`
	ArchivedAt    time.Time       `json:"archived_at,omitempty"`
	ArchiveReason string          `json:"archive_reason,omitempty"`
}

// DraftRevisionView is an immutable autosave/rebase checkpoint used for
// recovery, history, and three-way conflict presentation.
type DraftRevisionView struct {
	Org         string          `json:"org"`
	Workspace   string          `json:"workspace"`
	DraftID     string          `json:"draft_id"`
	FlowID      string          `json:"flow_id"`
	BaseVersion int             `json:"base_version"`
	Revision    int             `json:"revision"`
	Title       string          `json:"title"`
	Graph       events.Graph    `json:"graph"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Actor       string          `json:"actor"`
	At          time.Time       `json:"at"`
	Rebased     bool            `json:"rebased,omitempty"`
}

// ComponentVersionView is one immutable source and compiled component version.
type ComponentVersionView struct {
	Version        int                     `json:"version"`
	Etag           string                  `json:"etag"`
	SourceGraph    events.Graph            `json:"source_graph"`
	Graph          events.Graph            `json:"graph"`
	InputSchema    json.RawMessage         `json:"input_schema,omitempty"`
	OutputSchema   json.RawMessage         `json:"output_schema,omitempty"`
	Dependencies   []events.FlowDependency `json:"dependencies,omitempty"`
	Compatibility  CompatibilityReport     `json:"compatibility"`
	BreakingReason string                  `json:"breaking_change_reason,omitempty"`
	PublishedBy    string                  `json:"published_by"`
	PublishedAt    time.Time               `json:"published_at"`
}

// ComponentView is one reusable component and all immutable versions.
type ComponentView struct {
	Org         string                 `json:"org"`
	Workspace   string                 `json:"workspace"`
	ComponentID string                 `json:"component_id"`
	Slug        string                 `json:"slug"`
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Latest      int                    `json:"latest"`
	Versions    []ComponentVersionView `json:"versions"`
	Retired     bool                   `json:"retired"`
	CreatedBy   string                 `json:"created_by"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
	RetiredBy   string                 `json:"retired_by,omitempty"`
	RetiredAt   time.Time              `json:"retired_at,omitempty"`
}

// ChangeSetCheck is the latest result of one named check.
type ChangeSetCheck struct {
	Name       string      `json:"name"`
	Status     CheckStatus `json:"status"`
	Evidence   string      `json:"evidence,omitempty"`
	RecordedBy string      `json:"recorded_by"`
	RecordedAt time.Time   `json:"recorded_at"`
}

// ChangeSetReview is the current checker decision.
type ChangeSetReview struct {
	Decision ReviewDecision `json:"decision"`
	Reason   string         `json:"reason,omitempty"`
	Actor    string         `json:"actor"`
	At       time.Time      `json:"at"`
}

// ChangeSetView pins the exact draft revision and governance evidence.
type ChangeSetView struct {
	Org              string                    `json:"org"`
	Workspace        string                    `json:"workspace"`
	ChangeSetID      string                    `json:"changeset_id"`
	FlowID           string                    `json:"flow_id"`
	BaseVersion      int                       `json:"base_version"`
	DraftID          string                    `json:"draft_id"`
	DraftRevision    int                       `json:"draft_revision"`
	Title            string                    `json:"title"`
	Rationale        string                    `json:"rationale,omitempty"`
	State            ChangeSetState            `json:"state"`
	SourceGraph      events.Graph              `json:"source_graph"`
	Graph            events.Graph              `json:"graph"`
	InputSchema      json.RawMessage           `json:"input_schema,omitempty"`
	Dependencies     []events.FlowDependency   `json:"dependencies,omitempty"`
	ProposedEtag     string                    `json:"proposed_etag"`
	RequiredChecks   []string                  `json:"required_checks,omitempty"`
	Reviewers        []string                  `json:"reviewers,omitempty"`
	Checks           map[string]ChangeSetCheck `json:"checks,omitempty"`
	Review           *ChangeSetReview          `json:"review,omitempty"`
	CreatedBy        string                    `json:"created_by"`
	CreatedAt        time.Time                 `json:"created_at"`
	SubmittedBy      string                    `json:"submitted_by,omitempty"`
	SubmittedAt      time.Time                 `json:"submitted_at,omitempty"`
	UpdatedAt        time.Time                 `json:"updated_at"`
	PublishedBy      string                    `json:"published_by,omitempty"`
	PublishedAt      time.Time                 `json:"published_at,omitempty"`
	PublishedVersion int                       `json:"published_version,omitempty"`
	PublishedEtag    string                    `json:"published_etag,omitempty"`
}

// ComponentConsumer is an exact direct/transitive dependency observation.
type ComponentConsumer struct {
	Org              string `json:"org"`
	Workspace        string `json:"workspace"`
	ComponentID      string `json:"component_id"`
	ComponentVersion int    `json:"component_version"`
	ComponentEtag    string `json:"component_etag"`
	ConsumerKind     string `json:"consumer_kind"` // flow | component
	ConsumerID       string `json:"consumer_id"`
	ConsumerVersion  int    `json:"consumer_version"`
}

// Projector folds authoring events plus changeset-linked flow publications.
type Projector struct{}

func (Projector) Name() string { return "decision_authoring" }

func (Projector) Collections() []string {
	return []string{
		DraftCollection,
		DraftRevisionCollection,
		ComponentCollection,
		ChangeSetCollection,
		ComponentConsumerCollection,
		PresenceCollection,
	}
}

func (Projector) Apply(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	switch envelope.Type {
	case TypeDraftCreated:
		return applyDraftCreated(ctx, envelope, st)
	case TypeDraftSaved:
		return applyDraftSaved(ctx, envelope, st)
	case TypeDraftRebased:
		return applyDraftRebased(ctx, envelope, st)
	case TypeDraftArchived:
		return applyDraftArchived(ctx, envelope, st)
	case TypeComponentCreated:
		return applyComponentCreated(ctx, envelope, st)
	case TypeComponentVersionPublished:
		return applyComponentPublished(ctx, envelope, st)
	case TypeComponentRetired:
		return applyComponentRetired(ctx, envelope, st)
	case TypeChangeSetCreated:
		return applyChangeSetCreated(ctx, envelope, st)
	case TypeChangeSetSubmitted:
		return applyChangeSetSubmitted(ctx, envelope, st)
	case TypeChangeSetCheckRecorded:
		return applyChangeSetCheck(ctx, envelope, st)
	case TypeChangeSetReviewed:
		return applyChangeSetReviewed(ctx, envelope, st)
	case TypeChangeSetPublishRequested:
		return applyChangeSetPublishRequested(ctx, envelope, st)
	case events.TypeFlowVersionPublished:
		return applyLinkedFlowPublication(ctx, envelope, st)
	default:
		return nil
	}
}

func applyDraftCreated(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	payload, err := decode[DraftCreated](envelope)
	if err != nil {
		return err
	}
	view := DraftView{
		Org: envelope.Org, Workspace: envelope.Workspace,
		DraftID: payload.DraftID, FlowID: payload.FlowID,
		BaseVersion: payload.BaseVersion, Revision: payload.Revision,
		State: DraftStateActive, Title: payload.Title, Graph: payload.Graph,
		InputSchema: payload.InputSchema,
		CreatedBy:   envelope.Actor, CreatedAt: envelope.Time,
		UpdatedBy: envelope.Actor, UpdatedAt: envelope.Time,
	}
	if err := store.PutDoc(
		ctx, st, DraftCollection,
		store.Key(envelope.Org, envelope.Workspace, payload.DraftID), view,
	); err != nil {
		return err
	}
	return putDraftRevision(ctx, st, DraftRevisionView{
		Org: envelope.Org, Workspace: envelope.Workspace,
		DraftID: payload.DraftID, FlowID: payload.FlowID,
		BaseVersion: payload.BaseVersion, Revision: payload.Revision,
		Title: payload.Title, Graph: payload.Graph, InputSchema: payload.InputSchema,
		Actor: envelope.Actor, At: envelope.Time,
	})
}

func applyDraftSaved(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	payload, err := decode[DraftSaved](envelope)
	if err != nil {
		return err
	}
	var revision DraftRevisionView
	if err := mutateDraft(ctx, st, envelope, payload.DraftID, func(view *DraftView) error {
		if view.State != DraftStateActive || payload.Revision != view.Revision+1 {
			return fmt.Errorf(
				"authoring: invalid draft save %s revision %d after %d",
				view.State, payload.Revision, view.Revision,
			)
		}
		view.Revision, view.Title = payload.Revision, payload.Title
		view.Graph, view.InputSchema = payload.Graph, payload.InputSchema
		view.UpdatedBy = envelope.Actor
		revision = DraftRevisionView{
			Org: view.Org, Workspace: view.Workspace,
			DraftID: view.DraftID, FlowID: view.FlowID,
			BaseVersion: view.BaseVersion, Revision: payload.Revision,
			Title: payload.Title, Graph: payload.Graph, InputSchema: payload.InputSchema,
			Actor: envelope.Actor, At: envelope.Time,
		}
		return nil
	}); err != nil {
		return err
	}
	return putDraftRevision(ctx, st, revision)
}

func applyDraftRebased(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	payload, err := decode[DraftRebased](envelope)
	if err != nil {
		return err
	}
	var revision DraftRevisionView
	if err := mutateDraft(ctx, st, envelope, payload.DraftID, func(view *DraftView) error {
		if view.State != DraftStateActive || payload.Revision != view.Revision+1 {
			return fmt.Errorf(
				"authoring: invalid draft rebase %s revision %d after %d",
				view.State, payload.Revision, view.Revision,
			)
		}
		view.Revision, view.BaseVersion, view.Title = payload.Revision, payload.BaseVersion, payload.Title
		view.Graph, view.InputSchema = payload.Graph, payload.InputSchema
		view.UpdatedBy = envelope.Actor
		revision = DraftRevisionView{
			Org: view.Org, Workspace: view.Workspace,
			DraftID: view.DraftID, FlowID: view.FlowID,
			BaseVersion: payload.BaseVersion, Revision: payload.Revision,
			Title: payload.Title, Graph: payload.Graph, InputSchema: payload.InputSchema,
			Actor: envelope.Actor, At: envelope.Time, Rebased: true,
		}
		return nil
	}); err != nil {
		return err
	}
	return putDraftRevision(ctx, st, revision)
}

func applyDraftArchived(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	payload, err := decode[DraftArchived](envelope)
	if err != nil {
		return err
	}
	return mutateDraft(ctx, st, envelope, payload.DraftID, func(view *DraftView) error {
		if view.State != DraftStateActive {
			return fmt.Errorf("authoring: cannot archive draft from %s", view.State)
		}
		view.State, view.ArchivedBy, view.ArchivedAt = DraftStateArchived, envelope.Actor, envelope.Time
		view.ArchiveReason = payload.Reason
		return nil
	})
}

func applyComponentCreated(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	payload, err := decode[ComponentCreated](envelope)
	if err != nil {
		return err
	}
	return store.PutDoc(
		ctx, st, ComponentCollection,
		store.Key(envelope.Org, envelope.Workspace, payload.ComponentID),
		ComponentView{
			Org: envelope.Org, Workspace: envelope.Workspace,
			ComponentID: payload.ComponentID, Slug: payload.Slug,
			Name: payload.Name, Description: payload.Description,
			CreatedBy: envelope.Actor, CreatedAt: envelope.Time, UpdatedAt: envelope.Time,
		},
	)
}

func applyComponentPublished(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	payload, err := decode[ComponentVersionPublished](envelope)
	if err != nil {
		return err
	}
	if err := mutateComponent(ctx, st, envelope, payload.ComponentID, func(view *ComponentView) error {
		if view.Retired {
			return fmt.Errorf("authoring: cannot publish retired component %q", payload.ComponentID)
		}
		if payload.Version != view.Latest+1 {
			return fmt.Errorf(
				"authoring: component %q version %d follows %d",
				payload.ComponentID, payload.Version, view.Latest,
			)
		}
		view.Latest = payload.Version
		view.Versions = append(view.Versions, ComponentVersionView{
			Version: payload.Version, Etag: payload.Etag,
			SourceGraph: payload.SourceGraph, Graph: payload.Graph,
			InputSchema: payload.InputSchema, OutputSchema: payload.OutputSchema,
			Dependencies:   payload.Dependencies,
			Compatibility:  payload.Compatibility,
			BreakingReason: payload.BreakingReason,
			PublishedBy:    envelope.Actor, PublishedAt: envelope.Time,
		})
		return nil
	}); err != nil {
		return err
	}
	return putConsumers(
		ctx, st, envelope, payload.Dependencies,
		"component", payload.ComponentID, payload.Version,
	)
}

func applyComponentRetired(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	payload, err := decode[ComponentRetired](envelope)
	if err != nil {
		return err
	}
	return mutateComponent(ctx, st, envelope, payload.ComponentID, func(view *ComponentView) error {
		if view.Retired {
			return fmt.Errorf("authoring: component %q already retired", payload.ComponentID)
		}
		view.Retired, view.RetiredBy, view.RetiredAt = true, envelope.Actor, envelope.Time
		return nil
	})
}

func applyChangeSetCreated(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	payload, err := decode[ChangeSetCreated](envelope)
	if err != nil {
		return err
	}
	return store.PutDoc(
		ctx, st, ChangeSetCollection,
		store.Key(envelope.Org, envelope.Workspace, payload.ChangeSetID),
		newChangeSetView(payload, envelope),
	)
}

func newChangeSetView(
	payload ChangeSetCreated,
	envelope eventlog.Envelope,
) ChangeSetView {
	return ChangeSetView{
		Org: envelope.Org, Workspace: envelope.Workspace,
		ChangeSetID: payload.ChangeSetID, FlowID: payload.FlowID,
		BaseVersion: payload.BaseVersion, DraftID: payload.DraftID,
		DraftRevision: payload.DraftRevision, Title: payload.Title,
		Rationale: payload.Rationale, State: ChangeSetDraft,
		SourceGraph: payload.SourceGraph, Graph: payload.Graph,
		InputSchema: payload.InputSchema, Dependencies: payload.Dependencies,
		ProposedEtag:   payload.ProposedEtag,
		RequiredChecks: payload.RequiredChecks, Reviewers: payload.Reviewers,
		Checks:    make(map[string]ChangeSetCheck),
		CreatedBy: envelope.Actor, CreatedAt: envelope.Time, UpdatedAt: envelope.Time,
	}
}

func applyChangeSetSubmitted(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	payload, err := decode[ChangeSetSubmitted](envelope)
	if err != nil {
		return err
	}
	return mutateChangeSet(ctx, st, envelope, payload.ChangeSetID, func(view *ChangeSetView) error {
		if view.State != ChangeSetDraft {
			return fmt.Errorf("authoring: cannot submit changeset from %s", view.State)
		}
		view.State, view.SubmittedBy, view.SubmittedAt = ChangeSetInReview, envelope.Actor, envelope.Time
		view.Review = nil
		return nil
	})
}

func applyChangeSetCheck(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	payload, err := decode[ChangeSetCheckRecorded](envelope)
	if err != nil {
		return err
	}
	return mutateChangeSet(ctx, st, envelope, payload.ChangeSetID, func(view *ChangeSetView) error {
		if view.State == ChangeSetPublished || view.State == ChangeSetPublishing {
			return fmt.Errorf("authoring: cannot record a check in %s", view.State)
		}
		if view.Checks == nil {
			view.Checks = make(map[string]ChangeSetCheck)
		}
		view.Checks[payload.Name] = ChangeSetCheck{
			Name: payload.Name, Status: payload.Status, Evidence: payload.Evidence,
			RecordedBy: envelope.Actor, RecordedAt: envelope.Time,
		}
		return nil
	})
}

func applyChangeSetReviewed(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	payload, err := decode[ChangeSetReviewed](envelope)
	if err != nil {
		return err
	}
	return mutateChangeSet(ctx, st, envelope, payload.ChangeSetID, func(view *ChangeSetView) error {
		if view.State != ChangeSetInReview {
			return fmt.Errorf("authoring: cannot review changeset from %s", view.State)
		}
		if payload.Decision == ReviewApprove {
			view.State = ChangeSetApproved
		} else {
			view.State = ChangeSetChangesRequested
		}
		view.Review = &ChangeSetReview{
			Decision: payload.Decision, Reason: payload.Reason,
			Actor: envelope.Actor, At: envelope.Time,
		}
		return nil
	})
}

func applyChangeSetPublishRequested(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	payload, err := decode[ChangeSetPublishRequested](envelope)
	if err != nil {
		return err
	}
	return mutateChangeSet(ctx, st, envelope, payload.ChangeSetID, func(view *ChangeSetView) error {
		if view.State != ChangeSetApproved {
			return fmt.Errorf("authoring: cannot publish changeset from %s", view.State)
		}
		view.State = ChangeSetPublishing
		return nil
	})
}

func applyLinkedFlowPublication(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	payload, err := decode[events.FlowVersionPublished](envelope)
	if err != nil {
		return err
	}
	if payload.ChangeSetID == "" {
		return nil
	}
	if err := mutateChangeSet(ctx, st, envelope, payload.ChangeSetID, func(view *ChangeSetView) error {
		if view.State != ChangeSetPublishing && view.State != ChangeSetApproved {
			return fmt.Errorf(
				"authoring: flow publication for changeset %q from %s",
				payload.ChangeSetID, view.State,
			)
		}
		if view.FlowID != payload.FlowID ||
			view.DraftID != payload.DraftID ||
			view.DraftRevision != payload.DraftRevision {
			return fmt.Errorf("authoring: flow publication lineage does not match changeset %q", payload.ChangeSetID)
		}
		view.State = ChangeSetPublished
		view.PublishedBy, view.PublishedAt = envelope.Actor, envelope.Time
		view.PublishedVersion, view.PublishedEtag = payload.Version, payload.Etag
		return nil
	}); err != nil {
		return err
	}
	return putConsumers(
		ctx, st, envelope, payload.Dependencies,
		"flow", payload.FlowID, payload.Version,
	)
}

func putConsumers(
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	dependencies []events.FlowDependency,
	kind, consumerID string,
	consumerVersion int,
) error {
	for _, dependency := range dependencies {
		id := fmt.Sprintf(
			"%s@%d/%s:%s@%d",
			dependency.ComponentID, dependency.Version, kind, consumerID, consumerVersion,
		)
		if err := store.PutDoc(
			ctx, st, ComponentConsumerCollection,
			store.Key(envelope.Org, envelope.Workspace, id),
			ComponentConsumer{
				Org: envelope.Org, Workspace: envelope.Workspace,
				ComponentID:      dependency.ComponentID,
				ComponentVersion: dependency.Version,
				ComponentEtag:    dependency.Etag,
				ConsumerKind:     kind, ConsumerID: consumerID, ConsumerVersion: consumerVersion,
			},
		); err != nil {
			return err
		}
	}
	return nil
}

func mutateDraft(
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	draftID string,
	mutate func(*DraftView) error,
) error {
	return mutateView(ctx, st, envelope, DraftCollection, draftID, mutate)
}

func mutateComponent(
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	componentID string,
	mutate func(*ComponentView) error,
) error {
	return mutateView(ctx, st, envelope, ComponentCollection, componentID, mutate)
}

func mutateChangeSet(
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	changeSetID string,
	mutate func(*ChangeSetView) error,
) error {
	return mutateView(ctx, st, envelope, ChangeSetCollection, changeSetID, mutate)
}

func mutateView[T any](
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	collection, id string,
	mutate func(*T) error,
) error {
	var mutationErr error
	ok, err := store.UpdateDoc(ctx, st, collection, store.Key(envelope.Org, envelope.Workspace, id), func(view *T) {
		mutationErr = mutate(view)
		if mutationErr != nil {
			return
		}
		switch typed := any(view).(type) {
		case *DraftView:
			typed.UpdatedAt = envelope.Time
		case *ComponentView:
			typed.UpdatedAt = envelope.Time
		case *ChangeSetView:
			typed.UpdatedAt = envelope.Time
		}
	})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("authoring: event seq %d for unknown %s %q", envelope.Seq, collection, id)
	}
	return mutationErr
}

func decode[T any](envelope eventlog.Envelope) (T, error) {
	var payload T
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return payload, fmt.Errorf("authoring: decode %s seq %d: %w", envelope.Type, envelope.Seq, err)
	}
	return payload, nil
}

func ReadDraft(ctx context.Context, st store.Store, id identity.Identity, draftID string) (DraftView, bool, error) {
	return store.GetDoc[DraftView](ctx, st, DraftCollection, store.Key(id.Org, id.Workspace, draftID))
}

func ListDraftRevisions(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	draftID string,
) ([]DraftRevisionView, error) {
	return store.ListDocs[DraftRevisionView](
		ctx, st, DraftRevisionCollection,
		store.Key(id.Org, id.Workspace, draftID+"/"),
	)
}

func ListDrafts(ctx context.Context, st store.Store, id identity.Identity, flowID string) ([]DraftView, error) {
	return store.QueryDocs(
		ctx, st, DraftCollection, store.Key(id.Org, id.Workspace, ""),
		func(view DraftView) bool { return flowID == "" || view.FlowID == flowID },
		func(a, b DraftView) bool { return a.UpdatedAt.After(b.UpdatedAt) },
	)
}

func ReadComponent(ctx context.Context, st store.Store, id identity.Identity, componentID string) (ComponentView, bool, error) {
	return store.GetDoc[ComponentView](ctx, st, ComponentCollection, store.Key(id.Org, id.Workspace, componentID))
}

func ListComponents(ctx context.Context, st store.Store, id identity.Identity) ([]ComponentView, error) {
	return store.QueryDocs(
		ctx, st, ComponentCollection, store.Key(id.Org, id.Workspace, ""),
		nil,
		func(a, b ComponentView) bool { return a.Slug < b.Slug },
	)
}

func ReadChangeSet(ctx context.Context, st store.Store, id identity.Identity, changeSetID string) (ChangeSetView, bool, error) {
	return store.GetDoc[ChangeSetView](ctx, st, ChangeSetCollection, store.Key(id.Org, id.Workspace, changeSetID))
}

func ListChangeSets(ctx context.Context, st store.Store, id identity.Identity, flowID string) ([]ChangeSetView, error) {
	return store.QueryDocs(
		ctx, st, ChangeSetCollection, store.Key(id.Org, id.Workspace, ""),
		func(view ChangeSetView) bool { return flowID == "" || view.FlowID == flowID },
		func(a, b ChangeSetView) bool { return a.UpdatedAt.After(b.UpdatedAt) },
	)
}

func ListConsumers(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	componentID string,
	version int,
) ([]ComponentConsumer, error) {
	prefix := fmt.Sprintf("%s@%d/", componentID, version)
	items, err := store.ListDocs[ComponentConsumer](
		ctx, st, ComponentConsumerCollection,
		store.Key(id.Org, id.Workspace, prefix),
	)
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ConsumerKind == items[j].ConsumerKind {
			if items[i].ConsumerID == items[j].ConsumerID {
				return items[i].ConsumerVersion < items[j].ConsumerVersion
			}
			return items[i].ConsumerID < items[j].ConsumerID
		}
		return items[i].ConsumerKind < items[j].ConsumerKind
	})
	return items, nil
}

func putDraftRevision(
	ctx context.Context,
	st store.Store,
	revision DraftRevisionView,
) error {
	key := fmt.Sprintf("%s/%010d", revision.DraftID, revision.Revision)
	return store.PutDoc(
		ctx, st, DraftRevisionCollection,
		store.Key(revision.Org, revision.Workspace, key),
		revision,
	)
}
