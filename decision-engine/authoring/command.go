// SPDX-License-Identifier: AGPL-3.0-or-later

package authoring

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

const maxClaimRetries = 8

// FlowAuthor creates flow identities and records immutable compiled versions.
type FlowAuthor interface {
	CreateFlow(
		context.Context,
		identity.Identity,
		domain.CreateFlow,
	) (string, eventlog.Envelope, error)
	PublishVersion(
		context.Context,
		identity.Identity,
		domain.PublishVersion,
	) (int, string, eventlog.Envelope, error)
}

// Handler is the authoritative authoring command shell.
type Handler struct {
	log       eventlog.Log
	store     store.Store
	publisher FlowAuthor
	now       func() time.Time
	newID     func() string
	mu        sync.Mutex
}

func NewHandler(log eventlog.Log, st store.Store, publisher FlowAuthor) *Handler {
	return &Handler{
		log: log, store: st, publisher: publisher,
		now: func() time.Time { return time.Now().UTC() }, newID: randomID,
	}
}

// ImportResult is the durable authoring target created from one canonical
// repository asset. Import never publishes or deploys; the returned draft must
// pass through the same changeset/check/review path as a canvas-authored change.
type ImportResult struct {
	FlowID          string                   `json:"flow_id"`
	DraftID         string                   `json:"draft_id"`
	Revision        int                      `json:"revision"`
	Created         bool                     `json:"created"`
	MigrationReport CanonicalMigrationReport `json:"migration_report"`
	Event           eventlog.Envelope        `json:"-"`
}

// UpgradeDraftResult is one selected consumer's recoverable governed draft.
type UpgradeDraftResult struct {
	FlowID      string            `json:"flow_id"`
	DraftID     string            `json:"draft_id"`
	BaseVersion int               `json:"base_version"`
	Revision    int               `json:"revision"`
	Event       eventlog.Envelope `json:"-"`
}

// ValidateCanonicalImport performs every deterministic, target-workspace
// validation required before an import can append. Bundle import uses it to
// reject a bad later asset before an earlier asset is written.
func (h *Handler) ValidateCanonicalImport(
	ctx context.Context,
	id identity.Identity,
	asset CanonicalFlow,
	key string,
) error {
	if err := id.Valid(); err != nil {
		return err
	}
	normalized, err := NormalizeCanonicalFlow(asset)
	if err != nil {
		return err
	}
	catalog, err := h.foldComponents(ctx, id)
	if err != nil {
		return err
	}
	workspaceGraph, _, err := workspaceComponentReferences(
		normalized.Graph, catalog.bySlug,
	)
	if err != nil {
		return err
	}
	if _, _, err := Compile(workspaceGraph, catalog); err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 200 {
		return errors.New("authoring: idempotency key must contain 1..200 characters")
	}
	keyHash := hashText(id.Org + "\x00" + id.Workspace + "\x00" + id.Actor + "\x00" + key)
	requestHash, err := canonicalRequestHash(normalized)
	if err != nil {
		return err
	}
	prior, found, err := h.foldImportedDraft(ctx, id, keyHash)
	if err != nil {
		return err
	}
	if found && prior.requestHash != requestHash {
		return mutationKeyConflict("canonical import")
	}
	return nil
}

// ImportCanonicalFlow creates or resolves the flow identity and appends one
// revision-1 draft. key is mandatory at the HTTP boundary and makes a lost
// response safe to retry across replicas.
func (h *Handler) ImportCanonicalFlow(
	ctx context.Context,
	id identity.Identity,
	asset CanonicalFlow,
	key string,
) (ImportResult, error) {
	if err := h.ValidateCanonicalImport(ctx, id, asset, key); err != nil {
		return ImportResult{}, err
	}
	normalized, err := NormalizeCanonicalFlow(asset)
	if err != nil {
		return ImportResult{}, err
	}
	catalog, err := h.foldComponents(ctx, id)
	if err != nil {
		return ImportResult{}, err
	}
	workspaceGraph, migrationReport, err := workspaceComponentReferences(
		normalized.Graph, catalog.bySlug,
	)
	if err != nil {
		return ImportResult{}, err
	}
	if _, _, err := Compile(workspaceGraph, catalog); err != nil {
		return ImportResult{}, err
	}
	key = strings.TrimSpace(key)
	keyHash := hashText(id.Org + "\x00" + id.Workspace + "\x00" + id.Actor + "\x00" + key)
	requestHash, err := canonicalRequestHash(normalized)
	if err != nil {
		return ImportResult{}, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if prior, found, err := h.foldImportedDraft(ctx, id, keyHash); err != nil {
		return ImportResult{}, err
	} else if found {
		if prior.requestHash != requestHash {
			return ImportResult{}, mutationKeyConflict("canonical import")
		}
		prior.result.MigrationReport = migrationReport
		return prior.result, nil
	}

	flowID, latest, found, err := h.foldFlowBySlug(ctx, id, normalized.Slug)
	if err != nil {
		return ImportResult{}, err
	}
	created := false
	if !found {
		var createErr error
		flowID, _, createErr = h.publisher.CreateFlow(ctx, id, domain.CreateFlow{
			Slug: normalized.Slug, Name: normalized.Name,
			Description: normalized.Description,
		})
		if createErr != nil {
			// Another replica may have won the slug claim after our fold.
			flowID, latest, found, err = h.foldFlowBySlug(ctx, id, normalized.Slug)
			if err != nil {
				return ImportResult{}, err
			}
			if !found {
				return ImportResult{}, createErr
			}
		} else {
			created = true
		}
	}

	draftID := h.newID()
	event, err := eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeDraftCreated, h.now(),
		DraftCreated{
			DraftID: draftID, FlowID: flowID, BaseVersion: latest, Revision: 1,
			Title: normalized.Name, Graph: workspaceGraph,
			InputSchema:   normalized.InputSchema,
			ImportKeyHash: keyHash, ImportRequestHash: requestHash,
			ImportCreatedFlow: created,
		},
		"authoring.import\x00"+keyHash,
	)
	if errors.Is(err, eventlog.ErrConflict) {
		prior, found, foldErr := h.foldImportedDraft(ctx, id, keyHash)
		if foldErr != nil {
			return ImportResult{}, foldErr
		}
		if !found {
			return ImportResult{}, err
		}
		if prior.requestHash != requestHash {
			return ImportResult{}, mutationKeyConflict("canonical import")
		}
		prior.result.MigrationReport = migrationReport
		return prior.result, nil
	}
	if err != nil {
		return ImportResult{}, err
	}
	return ImportResult{
		FlowID: flowID, DraftID: draftID, Revision: 1,
		Created: created, MigrationReport: migrationReport, Event: event,
	}, nil
}

type importedDraft struct {
	result      ImportResult
	requestHash string
}

func (h *Handler) foldImportedDraft(
	ctx context.Context,
	id identity.Identity,
	keyHash string,
) (importedDraft, bool, error) {
	envelopes, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, Stream, 0)
	if err != nil {
		return importedDraft{}, false, err
	}
	for _, envelope := range envelopes {
		if envelope.Type != TypeDraftCreated {
			continue
		}
		payload, err := decode[DraftCreated](envelope)
		if err != nil {
			return importedDraft{}, false, err
		}
		if payload.ImportKeyHash == keyHash {
			return importedDraft{
				requestHash: payload.ImportRequestHash,
				result: ImportResult{
					FlowID: payload.FlowID, DraftID: payload.DraftID,
					Revision: payload.Revision, Created: payload.ImportCreatedFlow,
					Event: envelope,
				},
			}, true, nil
		}
	}
	return importedDraft{}, false, nil
}

func (h *Handler) foldFlowBySlug(
	ctx context.Context,
	id identity.Identity,
	slug string,
) (string, int, bool, error) {
	envelopes, err := h.log.ReadTenantStream(
		ctx, id.Org, id.Workspace, events.StreamFlows, 0,
	)
	if err != nil {
		return "", 0, false, err
	}
	flowID := ""
	latest := 0
	for _, envelope := range envelopes {
		switch envelope.Type {
		case events.TypeFlowCreated:
			payload, err := decode[events.FlowCreated](envelope)
			if err != nil {
				return "", 0, false, err
			}
			if payload.Slug == slug {
				flowID = payload.FlowID
			}
		case events.TypeFlowVersionPublished:
			payload, err := decode[events.FlowVersionPublished](envelope)
			if err != nil {
				return "", 0, false, err
			}
			if flowID != "" && payload.FlowID == flowID && payload.Version > latest {
				latest = payload.Version
			}
		}
	}
	return flowID, latest, flowID != "", nil
}

func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

func (h *Handler) CreateDraft(
	ctx context.Context,
	id identity.Identity,
	input DraftInput,
	key string,
) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if err := input.validate(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	keyHash, requestHash, err := mutationIdentity(
		id, "create-draft", input.FlowID, key, input,
	)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	if priorID, prior, found, err := h.foldDraftCreation(ctx, id, keyHash); err != nil {
		return "", eventlog.Envelope{}, err
	} else if found {
		if prior.MutationRequestHash != requestHash {
			return "", eventlog.Envelope{}, mutationKeyConflict("create draft")
		}
		return priorID, prior.Event, nil
	}
	flow, ok, err := h.foldFlow(ctx, id, input.FlowID)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	if !ok {
		return "", eventlog.Envelope{}, fmt.Errorf("authoring: unknown flow %q", input.FlowID)
	}
	if input.BaseVersion > flow.latest {
		return "", eventlog.Envelope{}, fmt.Errorf(
			"authoring: base version %d is newer than flow latest %d",
			input.BaseVersion, flow.latest,
		)
	}
	draftID := h.newID()
	event, err := eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeDraftCreated, h.now(),
		DraftCreated{
			DraftID: draftID, FlowID: input.FlowID,
			BaseVersion: input.BaseVersion, Revision: 1,
			Title: strings.TrimSpace(input.Title), Graph: input.Graph,
			InputSchema:     input.InputSchema,
			MutationKeyHash: keyHash, MutationRequestHash: requestHash,
		},
		"authoring.draft.create\x00"+keyHash,
	)
	if errors.Is(err, eventlog.ErrConflict) {
		priorID, prior, found, foldErr := h.foldDraftCreation(ctx, id, keyHash)
		if foldErr != nil {
			return "", eventlog.Envelope{}, foldErr
		}
		if found && prior.MutationRequestHash == requestHash {
			return priorID, prior.Event, nil
		}
		if found {
			return "", eventlog.Envelope{}, mutationKeyConflict("create draft")
		}
	}
	return draftID, event, err
}

func (h *Handler) SaveDraft(
	ctx context.Context,
	id identity.Identity,
	draftID string,
	input SaveDraftInput,
) (int, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return 0, eventlog.Envelope{}, err
	}
	if err := input.validate(); err != nil {
		return 0, eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; attempt < maxClaimRetries; attempt++ {
		draft, ok, err := h.foldDraft(ctx, id, draftID)
		if err != nil {
			return 0, eventlog.Envelope{}, err
		}
		if !ok {
			return 0, eventlog.Envelope{}, fmt.Errorf("authoring: unknown draft %q", draftID)
		}
		if draft.State != DraftStateActive {
			return 0, eventlog.Envelope{}, fmt.Errorf("authoring: draft %q is %s", draftID, draft.State)
		}
		if input.ExpectedRevision != draft.Revision {
			if input.ExpectedRevision+1 == draft.Revision &&
				draft.UpdatedBy == id.Actor &&
				draftSnapshotEqual(
					draft.Title, draft.Graph, draft.InputSchema,
					input.Title, input.Graph, input.InputSchema,
				) {
				return draft.Revision, eventlog.Envelope{}, nil
			}
			return 0, eventlog.Envelope{}, &RevisionConflict{Current: draft}
		}
		revision := draft.Revision + 1
		event, err := eventlog.AppendJSONUnique(
			ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeDraftSaved, h.now(),
			DraftSaved{
				DraftID: draftID, Revision: revision,
				Title: strings.TrimSpace(input.Title), Graph: input.Graph,
				InputSchema: input.InputSchema,
			},
			draftRevisionClaim(draftID, revision),
		)
		if errors.Is(err, eventlog.ErrConflict) {
			continue
		}
		return revision, event, err
	}
	current, _, err := h.foldDraft(ctx, id, draftID)
	if err != nil {
		return 0, eventlog.Envelope{}, err
	}
	return 0, eventlog.Envelope{}, &RevisionConflict{Current: current}
}

// RebaseDraft records the user's resolved snapshot against the current
// immutable flow version. It never attempts an automatic merge.
func (h *Handler) RebaseDraft(
	ctx context.Context,
	id identity.Identity,
	draftID string,
	input RebaseDraftInput,
) (int, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return 0, eventlog.Envelope{}, err
	}
	if err := input.validate(); err != nil {
		return 0, eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; attempt < maxClaimRetries; attempt++ {
		draft, ok, err := h.foldDraft(ctx, id, draftID)
		if err != nil {
			return 0, eventlog.Envelope{}, err
		}
		if !ok {
			return 0, eventlog.Envelope{}, fmt.Errorf("authoring: unknown draft %q", draftID)
		}
		if draft.State != DraftStateActive {
			return 0, eventlog.Envelope{}, fmt.Errorf("authoring: draft %q is %s", draftID, draft.State)
		}
		if input.ExpectedRevision != draft.Revision {
			if input.ExpectedRevision+1 == draft.Revision &&
				draft.UpdatedBy == id.Actor &&
				draft.BaseVersion == input.BaseVersion &&
				draftSnapshotEqual(
					draft.Title, draft.Graph, draft.InputSchema,
					input.Title, input.Graph, input.InputSchema,
				) {
				return draft.Revision, eventlog.Envelope{}, nil
			}
			return 0, eventlog.Envelope{}, &RevisionConflict{Current: draft}
		}
		flow, ok, err := h.foldFlow(ctx, id, draft.FlowID)
		if err != nil {
			return 0, eventlog.Envelope{}, err
		}
		if !ok || input.BaseVersion != flow.latest {
			return 0, eventlog.Envelope{}, fmt.Errorf(
				"authoring: rebase target %d is not current flow version %d",
				input.BaseVersion, flow.latest,
			)
		}
		revision := draft.Revision + 1
		event, err := eventlog.AppendJSONUnique(
			ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeDraftRebased, h.now(),
			DraftRebased{
				DraftID: draftID, Revision: revision,
				BaseVersion: input.BaseVersion, Title: strings.TrimSpace(input.Title),
				Graph: input.Graph, InputSchema: input.InputSchema,
			},
			draftRevisionClaim(draftID, revision),
		)
		if errors.Is(err, eventlog.ErrConflict) {
			continue
		}
		return revision, event, err
	}
	current, _, err := h.foldDraft(ctx, id, draftID)
	if err != nil {
		return 0, eventlog.Envelope{}, err
	}
	return 0, eventlog.Envelope{}, &RevisionConflict{Current: current}
}

func (h *Handler) ArchiveDraft(
	ctx context.Context,
	id identity.Identity,
	draftID string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	draft, ok, err := h.foldDraft(ctx, id, draftID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !ok {
		return eventlog.Envelope{}, fmt.Errorf("authoring: unknown draft %q", draftID)
	}
	if draft.State == DraftStateArchived {
		return eventlog.Envelope{}, nil
	}
	if draft.State != DraftStateActive {
		return eventlog.Envelope{}, fmt.Errorf("authoring: draft %q is %s", draftID, draft.State)
	}
	return eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeDraftArchived, h.now(),
		DraftArchived{DraftID: draftID, Reason: "archived by author"},
		"authoring.draft.archive\x00"+draftID,
	)
}

func (h *Handler) RecordDraftExport(
	ctx context.Context,
	id identity.Identity,
	draftID, flowID string,
	revision int,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(draftID) == "" || strings.TrimSpace(flowID) == "" || revision < 1 {
		return eventlog.Envelope{}, errors.New("authoring: valid draft export identity is required")
	}
	return eventlog.AppendJSON(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeDraftExported, h.now(),
		DraftExported{
			DraftID: draftID, FlowID: flowID, Revision: revision,
			Format: CanonicalFormatV1,
		},
	)
}

func (h *Handler) archiveStaleDraft(
	ctx context.Context,
	id identity.Identity,
	draftID string,
	cutoff time.Time,
) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	draft, ok, err := h.foldDraft(ctx, id, draftID)
	if err != nil {
		return false, err
	}
	if !ok || draft.State != DraftStateActive || draft.UpdatedAt.After(cutoff) {
		return false, nil
	}
	event, err := eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeDraftArchived, h.now(),
		DraftArchived{DraftID: draftID, Reason: "stale draft retention"},
		"authoring.draft.archive\x00"+draftID,
	)
	if errors.Is(err, eventlog.ErrConflict) {
		return false, nil
	}
	return event.ID != "", err
}

func (h *Handler) CreateComponent(
	ctx context.Context,
	id identity.Identity,
	input ComponentInput,
	key string,
) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if err := input.validate(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	keyHash, requestHash, err := mutationIdentity(
		id, "create-component", input.Slug, key, input,
	)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	if priorID, prior, found, err := h.foldComponentCreation(ctx, id, input.Slug); err != nil {
		return "", eventlog.Envelope{}, err
	} else if found {
		if prior.MutationRequestHash == requestHash && prior.MutationKeyHash == keyHash {
			return priorID, prior.Event, nil
		}
		return "", eventlog.Envelope{}, fmt.Errorf("authoring: component slug %q already exists", input.Slug)
	}
	componentID := h.newID()
	event, err := eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeComponentCreated, h.now(),
		ComponentCreated{
			ComponentID: componentID, Slug: input.Slug,
			Name: strings.TrimSpace(input.Name), Description: strings.TrimSpace(input.Description),
			MutationKeyHash: keyHash, MutationRequestHash: requestHash,
		},
		"authoring.component.slug\x00"+id.Org+"\x00"+id.Workspace+"\x00"+input.Slug,
	)
	if errors.Is(err, eventlog.ErrConflict) {
		priorID, prior, found, foldErr := h.foldComponentCreation(ctx, id, input.Slug)
		if foldErr != nil {
			return "", eventlog.Envelope{}, foldErr
		}
		if found && prior.MutationRequestHash == requestHash && prior.MutationKeyHash == keyHash {
			return priorID, prior.Event, nil
		}
		return "", eventlog.Envelope{}, fmt.Errorf("authoring: component slug %q already exists", input.Slug)
	}
	return componentID, event, err
}

func (h *Handler) PublishComponent(
	ctx context.Context,
	id identity.Identity,
	componentID string,
	input ComponentVersionInput,
	key string,
) (int, string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	if err := input.validate(); err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	keyHash, requestHash, err := mutationIdentity(
		id, "publish-component-version", componentID, key, input,
	)
	if err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; attempt < maxClaimRetries; attempt++ {
		catalog, err := h.foldComponents(ctx, id)
		if err != nil {
			return 0, "", eventlog.Envelope{}, err
		}
		component, ok := catalog.byID[componentID]
		if !ok {
			return 0, "", eventlog.Envelope{}, fmt.Errorf("authoring: unknown component %q", componentID)
		}
		if component.retired {
			return 0, "", eventlog.Envelope{}, fmt.Errorf("authoring: component %q is retired", componentID)
		}
		for _, prior := range component.versions {
			if prior.MutationKeyHash != keyHash {
				continue
			}
			if prior.MutationRequestHash != requestHash {
				return 0, "", eventlog.Envelope{}, mutationKeyConflict(
					"publish component version",
				)
			}
			return prior.Version, prior.Etag, prior.Event, nil
		}
		graph, dependencies, err := Compile(input.Graph, catalog)
		if err != nil {
			return 0, "", eventlog.Envelope{}, err
		}
		etag, err := componentEtag(graph, input, dependencies)
		if err != nil {
			return 0, "", eventlog.Envelope{}, err
		}
		version := len(component.versions) + 1
		compatibility := CompatibilityReport{
			ToVersion: version,
			Status:    CompatibilityInitial,
		}
		if version > 1 {
			previous := component.versions[version-2]
			compatibility, err = AssessComponentCompatibility(
				version-1, version,
				previous.InputSchema, previous.OutputSchema,
				input.InputSchema, input.OutputSchema,
			)
			if err != nil {
				return 0, "", eventlog.Envelope{}, err
			}
			if compatibility.Status == CompatibilityIncompatible && !input.AllowBreaking {
				return 0, "", eventlog.Envelope{}, &CompatibilityError{Report: compatibility}
			}
			if compatibility.Status == CompatibilityCompatible && input.AllowBreaking {
				return 0, "", eventlog.Envelope{}, errors.New(
					"authoring: allow_breaking is only valid when the server finds an incompatible contract",
				)
			}
		}
		event, err := eventlog.AppendJSONUnique(
			ctx, h.log, id.Org, id.Workspace, id.Actor, Stream,
			TypeComponentVersionPublished, h.now(),
			ComponentVersionPublished{
				ComponentID: componentID, Version: version, Etag: etag,
				SourceGraph: input.Graph, Graph: graph,
				InputSchema: input.InputSchema, OutputSchema: input.OutputSchema,
				Dependencies:    dependencies,
				Compatibility:   compatibility,
				BreakingReason:  strings.TrimSpace(input.BreakingChangeReason),
				MutationKeyHash: keyHash, MutationRequestHash: requestHash,
			},
			componentVersionClaim(componentID, version),
		)
		if errors.Is(err, eventlog.ErrConflict) {
			continue
		}
		return version, etag, event, err
	}
	return 0, "", eventlog.Envelope{}, fmt.Errorf(
		"authoring: component %q publish contention exceeded retry limit",
		componentID,
	)
}

// CreateComponentUpgradeDrafts replaces direct exact pins in selected flows
// after proving the target contract compatible. It creates drafts only; every
// result still requires checks, independent review, publication, and deployment.
func (h *Handler) CreateComponentUpgradeDrafts(
	ctx context.Context,
	id identity.Identity,
	componentID string,
	input ComponentUpgradeInput,
	key string,
) ([]UpgradeDraftResult, error) {
	if err := id.Valid(); err != nil {
		return nil, err
	}
	if err := input.validate(); err != nil {
		return nil, err
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 160 {
		return nil, errors.New(
			"authoring: upgrade idempotency key must contain 1..160 characters",
		)
	}
	catalog, err := h.foldComponents(ctx, id)
	if err != nil {
		return nil, err
	}
	component, ok := catalog.byID[componentID]
	if !ok {
		return nil, fmt.Errorf("authoring: unknown component %q", componentID)
	}
	if input.FromVersion > len(component.versions) || input.ToVersion > len(component.versions) {
		return nil, errors.New("authoring: component upgrade references an unknown version")
	}
	from, to := component.versions[input.FromVersion-1], component.versions[input.ToVersion-1]
	compatibility, err := AssessComponentCompatibility(
		input.FromVersion, input.ToVersion,
		from.InputSchema, from.OutputSchema, to.InputSchema, to.OutputSchema,
	)
	if err != nil {
		return nil, err
	}
	if compatibility.Status != CompatibilityCompatible {
		return nil, &CompatibilityError{Report: compatibility}
	}
	flowIDs := normalizeStrings(input.FlowIDs)
	if len(flowIDs) != len(input.FlowIDs) {
		return nil, errors.New("authoring: flow_ids must be unique and non-empty")
	}
	type plannedDraft struct {
		flowID      string
		baseVersion int
		graph       events.Graph
		inputSchema json.RawMessage
	}
	plans := make([]plannedDraft, 0, len(flowIDs))
	for _, flowID := range flowIDs {
		flow, found, err := h.foldFlow(ctx, id, flowID)
		if err != nil {
			return nil, err
		}
		if !found || flow.latest < 1 {
			return nil, fmt.Errorf("authoring: selected flow %q has no published version", flowID)
		}
		current := flow.versions[flow.latest]
		source := current.sourceGraph
		if len(source.Nodes) == 0 {
			source = current.graph
		}
		upgraded, direct, err := replaceDirectComponentPin(
			source, componentID, input.FromVersion, input.ToVersion,
		)
		if err != nil {
			return nil, err
		}
		if !direct {
			if hasDependency(current.dependencies, componentID, input.FromVersion) {
				return nil, fmt.Errorf(
					"authoring: flow %q consumes component %s@%d transitively; "+
						"upgrade its direct reusable-component parent first",
					flowID, componentID, input.FromVersion,
				)
			}
			return nil, fmt.Errorf(
				"authoring: flow %q current version does not consume component %s@%d",
				flowID, componentID, input.FromVersion,
			)
		}
		if _, _, err := Compile(upgraded, catalog); err != nil {
			return nil, fmt.Errorf("authoring: upgraded flow %q: %w", flowID, err)
		}
		plans = append(plans, plannedDraft{
			flowID: flowID, baseVersion: flow.latest,
			graph: upgraded, inputSchema: current.inputSchema,
		})
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = fmt.Sprintf(
			"Upgrade component %s from v%d to v%d",
			componentID, input.FromVersion, input.ToVersion,
		)
	}
	results := make([]UpgradeDraftResult, 0, len(plans))
	for _, plan := range plans {
		draftID, event, err := h.CreateDraft(ctx, id, DraftInput{
			FlowID: plan.flowID, BaseVersion: plan.baseVersion,
			Title: title, Graph: plan.graph, InputSchema: plan.inputSchema,
		}, hashText(key+"\x00"+plan.flowID))
		if err != nil {
			return nil, err
		}
		results = append(results, UpgradeDraftResult{
			FlowID: plan.flowID, DraftID: draftID,
			BaseVersion: plan.baseVersion, Revision: 1, Event: event,
		})
	}
	return results, nil
}

func replaceDirectComponentPin(
	graph events.Graph,
	componentID string,
	fromVersion, toVersion int,
) (events.Graph, bool, error) {
	out := cloneGraph(graph)
	replaced := false
	for index := range out.Nodes {
		node := &out.Nodes[index]
		if node.Type != events.NodeSubflow {
			continue
		}
		var config SubflowConfig
		if err := json.Unmarshal(node.Config, &config); err != nil {
			return events.Graph{}, false, fmt.Errorf(
				"authoring: subflow node %q config: %w", node.ID, err,
			)
		}
		if config.ComponentID != componentID || config.Version != fromVersion {
			continue
		}
		config.Version = toVersion
		encoded, err := json.Marshal(config)
		if err != nil {
			return events.Graph{}, false, err
		}
		node.Config = encoded
		replaced = true
	}
	return out, replaced, nil
}

func hasDependency(
	dependencies []events.FlowDependency,
	componentID string,
	version int,
) bool {
	for _, dependency := range dependencies {
		if dependency.ComponentID == componentID && dependency.Version == version {
			return true
		}
	}
	return false
}

func (h *Handler) RetireComponent(
	ctx context.Context,
	id identity.Identity,
	componentID string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	catalog, err := h.foldComponents(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	component, ok := catalog.byID[componentID]
	if !ok {
		return eventlog.Envelope{}, fmt.Errorf("authoring: unknown component %q", componentID)
	}
	if component.retired {
		return eventlog.Envelope{}, nil
	}
	return eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeComponentRetired, h.now(),
		ComponentRetired{ComponentID: componentID},
		"authoring.component.retire\x00"+componentID,
	)
}

func (h *Handler) CreateChangeSet(
	ctx context.Context,
	id identity.Identity,
	input ChangeSetInput,
	key string,
) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if err := input.validate(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	keyHash, requestHash, err := mutationIdentity(
		id, "create-changeset", input.DraftID, key, input,
	)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	if priorID, prior, found, err := h.foldChangeSetCreation(ctx, id, keyHash); err != nil {
		return "", eventlog.Envelope{}, err
	} else if found {
		if prior.MutationRequestHash != requestHash {
			return "", eventlog.Envelope{}, mutationKeyConflict("create changeset")
		}
		return priorID, prior.Event, nil
	}
	draft, ok, err := h.foldDraft(ctx, id, input.DraftID)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	if !ok {
		return "", eventlog.Envelope{}, fmt.Errorf("authoring: unknown draft %q", input.DraftID)
	}
	if draft.State != DraftStateActive {
		return "", eventlog.Envelope{}, fmt.Errorf("authoring: draft %q is %s", input.DraftID, draft.State)
	}
	if draft.Revision != input.DraftRevision {
		return "", eventlog.Envelope{}, &RevisionConflict{Current: draft}
	}
	catalog, err := h.foldComponents(ctx, id)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	graph, dependencies, err := Compile(draft.Graph, catalog)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	source := cloneGraph(draft.Graph)
	proposedEtag, err := domain.EtagWithSource(
		graph, draft.InputSchema, &source, dependencies,
	)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	reviewers := normalizeStrings(input.Reviewers)
	if contains(reviewers, id.Actor) {
		return "", eventlog.Envelope{}, errors.New(
			"authoring: changeset creator cannot be assigned as their own reviewer",
		)
	}
	changeSetID := h.newID()
	event, err := eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeChangeSetCreated, h.now(),
		ChangeSetCreated{
			ChangeSetID: changeSetID, FlowID: draft.FlowID,
			BaseVersion: draft.BaseVersion, DraftID: draft.DraftID,
			DraftRevision: draft.Revision, Title: strings.TrimSpace(input.Title),
			Rationale:       strings.TrimSpace(input.Rationale),
			SourceGraph:     source,
			Graph:           graph,
			InputSchema:     draft.InputSchema,
			Dependencies:    dependencies,
			ProposedEtag:    proposedEtag,
			RequiredChecks:  normalizeStrings(input.RequiredChecks),
			Reviewers:       reviewers,
			MutationKeyHash: keyHash, MutationRequestHash: requestHash,
		},
		"authoring.changeset.create\x00"+keyHash,
	)
	if errors.Is(err, eventlog.ErrConflict) {
		priorID, prior, found, foldErr := h.foldChangeSetCreation(ctx, id, keyHash)
		if foldErr != nil {
			return "", eventlog.Envelope{}, foldErr
		}
		if found && prior.MutationRequestHash == requestHash {
			return priorID, prior.Event, nil
		}
		if found {
			return "", eventlog.Envelope{}, mutationKeyConflict("create changeset")
		}
	}
	return changeSetID, event, err
}

func (h *Handler) SubmitChangeSet(
	ctx context.Context,
	id identity.Identity,
	changeSetID string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	changeSet, ok, err := h.foldChangeSet(ctx, id, changeSetID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !ok {
		return eventlog.Envelope{}, fmt.Errorf("authoring: unknown changeset %q", changeSetID)
	}
	if changeSet.State == ChangeSetInReview && changeSet.SubmittedBy == id.Actor {
		return eventlog.Envelope{}, nil
	}
	if changeSet.State != ChangeSetDraft {
		return eventlog.Envelope{}, fmt.Errorf("authoring: cannot submit changeset from %s", changeSet.State)
	}
	if err := h.validatePinnedChangeSet(ctx, id, changeSet); err != nil {
		return eventlog.Envelope{}, err
	}
	return eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeChangeSetSubmitted, h.now(),
		ChangeSetSubmitted{
			ChangeSetID: changeSetID, FlowID: changeSet.FlowID,
			Title: changeSet.Title, CreatedBy: changeSet.CreatedBy,
			Reviewers: changeSet.Reviewers,
		},
		"authoring.changeset.submit\x00"+changeSetID,
	)
}

func (h *Handler) RecordCheck(
	ctx context.Context,
	id identity.Identity,
	changeSetID string,
	input CheckInput,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := input.validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	changeSet, ok, err := h.foldChangeSet(ctx, id, changeSetID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !ok {
		return eventlog.Envelope{}, fmt.Errorf("authoring: unknown changeset %q", changeSetID)
	}
	if changeSet.State != ChangeSetDraft && changeSet.State != ChangeSetInReview {
		return eventlog.Envelope{}, fmt.Errorf("authoring: cannot record a check in %s", changeSet.State)
	}
	if strings.TrimSpace(input.Name) == "flow-validation" {
		if err := h.validatePinnedChangeSet(ctx, id, changeSet); err != nil {
			return eventlog.Envelope{}, err
		}
		input.Status = CheckPassed
		input.Evidence = "Server compiled the pinned source and verified its exact dependency and content hashes."
	}
	if existing, found := changeSet.Checks[strings.TrimSpace(input.Name)]; found &&
		existing.Status == input.Status &&
		existing.Evidence == strings.TrimSpace(input.Evidence) &&
		existing.RecordedBy == id.Actor {
		return eventlog.Envelope{}, nil
	}
	return eventlog.AppendJSON(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeChangeSetCheckRecorded, h.now(),
		ChangeSetCheckRecorded{
			ChangeSetID: changeSetID, Name: strings.TrimSpace(input.Name),
			Status: input.Status, Evidence: strings.TrimSpace(input.Evidence),
		},
	)
}

func (h *Handler) ReviewChangeSet(
	ctx context.Context,
	id identity.Identity,
	changeSetID string,
	input ReviewInput,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := input.validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	changeSet, ok, err := h.foldChangeSet(ctx, id, changeSetID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !ok {
		return eventlog.Envelope{}, fmt.Errorf("authoring: unknown changeset %q", changeSetID)
	}
	if changeSet.Review != nil &&
		changeSet.Review.Actor == id.Actor &&
		changeSet.Review.Decision == input.Decision &&
		changeSet.Review.Reason == strings.TrimSpace(input.Reason) {
		return eventlog.Envelope{}, nil
	}
	if changeSet.State != ChangeSetInReview {
		return eventlog.Envelope{}, fmt.Errorf("authoring: cannot review changeset from %s", changeSet.State)
	}
	if id.Actor == changeSet.CreatedBy || id.Actor == changeSet.SubmittedBy {
		return eventlog.Envelope{}, errors.New("authoring: maker-checker requires a different reviewer")
	}
	if len(changeSet.Reviewers) > 0 && !contains(changeSet.Reviewers, id.Actor) {
		return eventlog.Envelope{}, errors.New("authoring: caller is not an assigned reviewer")
	}
	if input.Decision == ReviewApprove {
		if err := h.validatePinnedChangeSet(ctx, id, changeSet); err != nil {
			return eventlog.Envelope{}, err
		}
		for _, required := range changeSet.RequiredChecks {
			check, ok := changeSet.Checks[required]
			if !ok || check.Status != CheckPassed {
				return eventlog.Envelope{}, fmt.Errorf(
					"authoring: required check %q has not passed",
					required,
				)
			}
		}
	}
	return eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeChangeSetReviewed, h.now(),
		ChangeSetReviewed{
			ChangeSetID: changeSetID, FlowID: changeSet.FlowID,
			Title: changeSet.Title, CreatedBy: changeSet.CreatedBy,
			Decision: input.Decision,
			Reason:   strings.TrimSpace(input.Reason),
		},
		"authoring.changeset.review\x00"+changeSetID,
	)
}

func (h *Handler) remindChangeSet(
	ctx context.Context,
	id identity.Identity,
	changeSetID string,
	day string,
) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	changeSet, ok, err := h.foldChangeSet(ctx, id, changeSetID)
	if err != nil {
		return false, err
	}
	if !ok || changeSet.State != ChangeSetInReview {
		return false, nil
	}
	event, err := eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream,
		TypeChangeSetReviewReminded, h.now(),
		ChangeSetReviewReminded{
			ChangeSetID: changeSetID, FlowID: changeSet.FlowID,
			Title: changeSet.Title, CreatedBy: changeSet.CreatedBy,
			Reviewers: changeSet.Reviewers,
		},
		"authoring.changeset.reminder\x00"+changeSetID+"\x00"+day,
	)
	if errors.Is(err, eventlog.ErrConflict) {
		return false, nil
	}
	return event.ID != "", err
}

// PublishChangeSet records one durable publication request, then reconciles it
// synchronously. A scheduler can call ReconcilePublications after a crash.
func (h *Handler) PublishChangeSet(
	ctx context.Context,
	id identity.Identity,
	changeSetID string,
) (int, string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	h.mu.Lock()
	changeSet, ok, err := h.foldChangeSet(ctx, id, changeSetID)
	if err != nil {
		h.mu.Unlock()
		return 0, "", eventlog.Envelope{}, err
	}
	if !ok {
		h.mu.Unlock()
		return 0, "", eventlog.Envelope{}, fmt.Errorf("authoring: unknown changeset %q", changeSetID)
	}
	switch changeSet.State {
	case ChangeSetPublished:
		h.mu.Unlock()
		return changeSet.PublishedVersion, changeSet.PublishedEtag, eventlog.Envelope{}, nil
	case ChangeSetApproved:
		if err := h.validatePinnedChangeSet(ctx, id, changeSet); err != nil {
			h.mu.Unlock()
			return 0, "", eventlog.Envelope{}, err
		}
		_, err = eventlog.AppendJSONUnique(
			ctx, h.log, id.Org, id.Workspace, id.Actor, Stream,
			TypeChangeSetPublishRequested, h.now(),
			ChangeSetPublishRequested{ChangeSetID: changeSetID},
			"authoring.changeset.publish\x00"+changeSetID,
		)
		if err != nil && !errors.Is(err, eventlog.ErrConflict) {
			h.mu.Unlock()
			return 0, "", eventlog.Envelope{}, err
		}
	case ChangeSetPublishing:
		// A prior caller recorded the durable request; resume it below.
	default:
		h.mu.Unlock()
		return 0, "", eventlog.Envelope{}, fmt.Errorf("authoring: cannot publish changeset from %s", changeSet.State)
	}
	h.mu.Unlock()
	return h.publishRequested(ctx, id, changeSetID)
}

// ReconcilePublications resumes every durable publication request for a tenant.
func (h *Handler) ReconcilePublications(
	ctx context.Context,
	id identity.Identity,
) (int, error) {
	changeSets, err := h.foldChangeSets(ctx, id)
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0)
	for changeSetID, changeSet := range changeSets {
		if changeSet.State == ChangeSetPublishing {
			ids = append(ids, changeSetID)
		}
	}
	sort.Strings(ids)
	for _, changeSetID := range ids {
		if _, _, _, err := h.publishRequested(ctx, id, changeSetID); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

func (h *Handler) publishRequested(
	ctx context.Context,
	id identity.Identity,
	changeSetID string,
) (int, string, eventlog.Envelope, error) {
	changeSet, ok, err := h.foldChangeSet(ctx, id, changeSetID)
	if err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	if !ok {
		return 0, "", eventlog.Envelope{}, fmt.Errorf("authoring: unknown changeset %q", changeSetID)
	}
	if changeSet.State == ChangeSetPublished {
		return changeSet.PublishedVersion, changeSet.PublishedEtag, eventlog.Envelope{}, nil
	}
	if changeSet.State != ChangeSetPublishing {
		return 0, "", eventlog.Envelope{}, fmt.Errorf("authoring: changeset %q is %s", changeSetID, changeSet.State)
	}
	source := cloneGraph(changeSet.SourceGraph)
	return h.publisher.PublishVersion(ctx, id, domain.PublishVersion{
		FlowID: changeSet.FlowID, Graph: changeSet.Graph, InputSchema: changeSet.InputSchema,
		SourceGraph: &source, Dependencies: changeSet.Dependencies,
		ChangeSetID: changeSet.ChangeSetID, DraftID: changeSet.DraftID,
		DraftRevision: changeSet.DraftRevision,
	})
}

// DifferenceForChangeSet compares the pinned base version and draft revision.
func (h *Handler) DifferenceForChangeSet(
	ctx context.Context,
	id identity.Identity,
	changeSetID string,
) ([]Difference, error) {
	changeSet, ok, err := h.foldChangeSet(ctx, id, changeSetID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("authoring: unknown changeset %q", changeSetID)
	}
	flow, ok, err := h.foldFlow(ctx, id, changeSet.FlowID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("authoring: unknown flow %q", changeSet.FlowID)
	}
	before := flowVersionAggregate{}
	if changeSet.BaseVersion > 0 {
		version, ok := flow.versions[changeSet.BaseVersion]
		if !ok {
			return nil, fmt.Errorf("authoring: unknown base version %d", changeSet.BaseVersion)
		}
		before = version
	}
	return SemanticChangeSetDiff(
		before.graph, before.inputSchema, before.dependencies,
		changeSet.SourceGraph, changeSet.InputSchema, changeSet.Dependencies,
	)
}

func (h *Handler) validatePinnedChangeSet(
	ctx context.Context,
	id identity.Identity,
	changeSet ChangeSetView,
) error {
	flow, ok, err := h.foldFlow(ctx, id, changeSet.FlowID)
	if err != nil {
		return err
	}
	if !ok || flow.latest != changeSet.BaseVersion {
		return fmt.Errorf(
			"authoring: changeset base version %d is stale; flow latest is %d",
			changeSet.BaseVersion, flow.latest,
		)
	}
	catalog, err := h.foldComponents(ctx, id)
	if err != nil {
		return err
	}
	graph, dependencies, err := Compile(changeSet.SourceGraph, catalog)
	if err != nil {
		return err
	}
	source := cloneGraph(changeSet.SourceGraph)
	recompiledEtag, err := domain.EtagWithSource(
		graph, changeSet.InputSchema, &source, dependencies,
	)
	if err != nil {
		return err
	}
	storedEtag, err := domain.EtagWithSource(
		changeSet.Graph, changeSet.InputSchema, &source, changeSet.Dependencies,
	)
	if err != nil {
		return err
	}
	if recompiledEtag != changeSet.ProposedEtag ||
		storedEtag != changeSet.ProposedEtag {
		return errors.New("authoring: pinned changeset content no longer matches its reviewed hash")
	}
	return nil
}

type flowAggregate struct {
	latest     int
	versions   map[int]flowVersionAggregate
	changeSets map[string]struct {
		version int
		etag    string
	}
}

type flowVersionAggregate struct {
	graph        events.Graph
	sourceGraph  events.Graph
	inputSchema  json.RawMessage
	dependencies []events.FlowDependency
}

func (h *Handler) foldFlow(
	ctx context.Context,
	id identity.Identity,
	flowID string,
) (flowAggregate, bool, error) {
	envelopes, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, events.StreamFlows, 0)
	if err != nil {
		return flowAggregate{}, false, err
	}
	aggregate := flowAggregate{
		versions: make(map[int]flowVersionAggregate),
		changeSets: make(map[string]struct {
			version int
			etag    string
		}),
	}
	found := false
	for _, envelope := range envelopes {
		switch envelope.Type {
		case events.TypeFlowCreated:
			payload, err := decode[events.FlowCreated](envelope)
			if err != nil {
				return flowAggregate{}, false, err
			}
			if payload.FlowID == flowID {
				found = true
			}
		case events.TypeFlowVersionPublished:
			payload, err := decode[events.FlowVersionPublished](envelope)
			if err != nil {
				return flowAggregate{}, false, err
			}
			if payload.FlowID != flowID {
				continue
			}
			aggregate.versions[payload.Version] = flowVersionAggregate{
				graph: payload.Graph, inputSchema: payload.InputSchema,
				dependencies: payload.Dependencies,
			}
			if payload.SourceGraph != nil {
				version := aggregate.versions[payload.Version]
				version.sourceGraph = cloneGraph(*payload.SourceGraph)
				aggregate.versions[payload.Version] = version
			}
			if payload.Version > aggregate.latest {
				aggregate.latest = payload.Version
			}
			if payload.ChangeSetID != "" {
				aggregate.changeSets[payload.ChangeSetID] = struct {
					version int
					etag    string
				}{version: payload.Version, etag: payload.Etag}
			}
		}
	}
	return aggregate, found, nil
}

type componentAggregate struct {
	id       string
	slug     string
	retired  bool
	versions []ComponentVersion
}

type componentCatalog struct {
	byID   map[string]*componentAggregate
	bySlug map[string]string
}

func (c componentCatalog) ResolveComponent(componentID string, version int) (ComponentVersion, bool) {
	component, ok := c.byID[componentID]
	if !ok || version < 1 || version > len(component.versions) {
		return ComponentVersion{}, false
	}
	return component.versions[version-1], true
}

func (h *Handler) foldComponents(
	ctx context.Context,
	id identity.Identity,
) (componentCatalog, error) {
	envelopes, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, Stream, 0)
	if err != nil {
		return componentCatalog{}, err
	}
	catalog := componentCatalog{
		byID:   make(map[string]*componentAggregate),
		bySlug: make(map[string]string),
	}
	for _, envelope := range envelopes {
		switch envelope.Type {
		case TypeComponentCreated:
			payload, err := decode[ComponentCreated](envelope)
			if err != nil {
				return componentCatalog{}, err
			}
			catalog.byID[payload.ComponentID] = &componentAggregate{
				id: payload.ComponentID, slug: payload.Slug,
			}
			catalog.bySlug[payload.Slug] = payload.ComponentID
		case TypeComponentVersionPublished:
			payload, err := decode[ComponentVersionPublished](envelope)
			if err != nil {
				return componentCatalog{}, err
			}
			component := catalog.byID[payload.ComponentID]
			if component == nil {
				return componentCatalog{}, fmt.Errorf("authoring: component event for unknown %q", payload.ComponentID)
			}
			component.versions = append(component.versions, ComponentVersion{
				ComponentID: payload.ComponentID, Version: payload.Version,
				Etag: payload.Etag, SourceGraph: payload.SourceGraph,
				InputSchema: payload.InputSchema, OutputSchema: payload.OutputSchema,
				Event:               envelope,
				MutationKeyHash:     payload.MutationKeyHash,
				MutationRequestHash: payload.MutationRequestHash,
			})
		case TypeComponentRetired:
			payload, err := decode[ComponentRetired](envelope)
			if err != nil {
				return componentCatalog{}, err
			}
			if component := catalog.byID[payload.ComponentID]; component != nil {
				component.retired = true
			}
		}
	}
	return catalog, nil
}

type mutationCreation struct {
	MutationKeyHash     string
	MutationRequestHash string
	Event               eventlog.Envelope
}

func (h *Handler) foldDraftCreation(
	ctx context.Context,
	id identity.Identity,
	keyHash string,
) (string, mutationCreation, bool, error) {
	return h.foldMutationCreation(ctx, id, TypeDraftCreated, keyHash)
}

func (h *Handler) foldComponentCreation(
	ctx context.Context,
	id identity.Identity,
	slug string,
) (string, mutationCreation, bool, error) {
	return h.foldMutationCreation(ctx, id, TypeComponentCreated, slug)
}

func (h *Handler) foldChangeSetCreation(
	ctx context.Context,
	id identity.Identity,
	keyHash string,
) (string, mutationCreation, bool, error) {
	return h.foldMutationCreation(ctx, id, TypeChangeSetCreated, keyHash)
}

func (h *Handler) foldMutationCreation(
	ctx context.Context,
	id identity.Identity,
	eventType, matchValue string,
) (string, mutationCreation, bool, error) {
	envelopes, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, Stream, 0)
	if err != nil {
		return "", mutationCreation{}, false, err
	}
	for _, envelope := range envelopes {
		if envelope.Type != eventType {
			continue
		}
		switch eventType {
		case TypeDraftCreated:
			payload, err := decode[DraftCreated](envelope)
			if err != nil {
				return "", mutationCreation{}, false, err
			}
			if payload.MutationKeyHash == matchValue {
				return creationMatch(
					payload.DraftID, payload.MutationKeyHash,
					payload.MutationRequestHash, envelope,
				)
			}
		case TypeComponentCreated:
			payload, err := decode[ComponentCreated](envelope)
			if err != nil {
				return "", mutationCreation{}, false, err
			}
			if payload.Slug == matchValue {
				return creationMatch(
					payload.ComponentID, payload.MutationKeyHash,
					payload.MutationRequestHash, envelope,
				)
			}
		case TypeChangeSetCreated:
			payload, err := decode[ChangeSetCreated](envelope)
			if err != nil {
				return "", mutationCreation{}, false, err
			}
			if payload.MutationKeyHash == matchValue {
				return creationMatch(
					payload.ChangeSetID, payload.MutationKeyHash,
					payload.MutationRequestHash, envelope,
				)
			}
		default:
			return "", mutationCreation{}, false, fmt.Errorf(
				"authoring: unsupported creation event %q", eventType,
			)
		}
	}
	return "", mutationCreation{}, false, nil
}

func creationMatch(
	resourceID, keyHash, requestHash string,
	envelope eventlog.Envelope,
) (string, mutationCreation, bool, error) {
	return resourceID, mutationCreation{
		MutationKeyHash: keyHash, MutationRequestHash: requestHash,
		Event: envelope,
	}, true, nil
}

func (h *Handler) foldDraft(
	ctx context.Context,
	id identity.Identity,
	draftID string,
) (DraftView, bool, error) {
	envelopes, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, Stream, 0)
	if err != nil {
		return DraftView{}, false, err
	}
	var view DraftView
	found := false
	for _, envelope := range envelopes {
		switch envelope.Type {
		case TypeDraftCreated:
			payload, err := decode[DraftCreated](envelope)
			if err != nil {
				return DraftView{}, false, err
			}
			if payload.DraftID != draftID {
				continue
			}
			found = true
			view = DraftView{
				Org: id.Org, Workspace: id.Workspace,
				DraftID: payload.DraftID, FlowID: payload.FlowID,
				BaseVersion: payload.BaseVersion, Revision: payload.Revision,
				State: DraftStateActive, Title: payload.Title,
				Graph: payload.Graph, InputSchema: payload.InputSchema,
				CreatedBy: envelope.Actor, CreatedAt: envelope.Time,
				UpdatedBy: envelope.Actor, UpdatedAt: envelope.Time,
			}
		case TypeDraftSaved:
			payload, err := decode[DraftSaved](envelope)
			if err != nil {
				return DraftView{}, false, err
			}
			if payload.DraftID == draftID && found {
				view.Revision, view.Title = payload.Revision, payload.Title
				view.Graph, view.InputSchema = payload.Graph, payload.InputSchema
				view.UpdatedBy, view.UpdatedAt = envelope.Actor, envelope.Time
			}
		case TypeDraftRebased:
			payload, err := decode[DraftRebased](envelope)
			if err != nil {
				return DraftView{}, false, err
			}
			if payload.DraftID == draftID && found {
				view.Revision, view.BaseVersion, view.Title =
					payload.Revision, payload.BaseVersion, payload.Title
				view.Graph, view.InputSchema = payload.Graph, payload.InputSchema
				view.UpdatedBy, view.UpdatedAt = envelope.Actor, envelope.Time
			}
		case TypeDraftArchived:
			payload, err := decode[DraftArchived](envelope)
			if err != nil {
				return DraftView{}, false, err
			}
			if payload.DraftID == draftID && found {
				view.State = DraftStateArchived
				view.ArchivedBy, view.ArchivedAt = envelope.Actor, envelope.Time
				view.ArchiveReason = payload.Reason
				view.UpdatedAt = envelope.Time
			}
		}
	}
	return view, found, nil
}

func (h *Handler) foldChangeSet(
	ctx context.Context,
	id identity.Identity,
	changeSetID string,
) (ChangeSetView, bool, error) {
	all, err := h.foldChangeSets(ctx, id)
	if err != nil {
		return ChangeSetView{}, false, err
	}
	view, ok := all[changeSetID]
	return view, ok, nil
}

func (h *Handler) foldChangeSets(
	ctx context.Context,
	id identity.Identity,
) (map[string]ChangeSetView, error) {
	envelopes, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, Stream, 0)
	if err != nil {
		return nil, err
	}
	views := make(map[string]ChangeSetView)
	for _, envelope := range envelopes {
		switch envelope.Type {
		case TypeChangeSetCreated:
			payload, err := decode[ChangeSetCreated](envelope)
			if err != nil {
				return nil, err
			}
			views[payload.ChangeSetID] = newChangeSetView(payload, envelope)
		case TypeChangeSetSubmitted:
			payload, err := decode[ChangeSetSubmitted](envelope)
			if err != nil {
				return nil, err
			}
			view, ok := views[payload.ChangeSetID]
			if ok {
				view.State, view.SubmittedBy, view.SubmittedAt = ChangeSetInReview, envelope.Actor, envelope.Time
				view.UpdatedAt = envelope.Time
				views[payload.ChangeSetID] = view
			}
		case TypeChangeSetCheckRecorded:
			payload, err := decode[ChangeSetCheckRecorded](envelope)
			if err != nil {
				return nil, err
			}
			view, ok := views[payload.ChangeSetID]
			if ok {
				view.Checks[payload.Name] = ChangeSetCheck{
					Name: payload.Name, Status: payload.Status, Evidence: payload.Evidence,
					RecordedBy: envelope.Actor, RecordedAt: envelope.Time,
				}
				view.UpdatedAt = envelope.Time
				views[payload.ChangeSetID] = view
			}
		case TypeChangeSetReviewed:
			payload, err := decode[ChangeSetReviewed](envelope)
			if err != nil {
				return nil, err
			}
			view, ok := views[payload.ChangeSetID]
			if ok {
				if payload.Decision == ReviewApprove {
					view.State = ChangeSetApproved
				} else {
					view.State = ChangeSetChangesRequested
				}
				view.Review = &ChangeSetReview{
					Decision: payload.Decision, Reason: payload.Reason,
					Actor: envelope.Actor, At: envelope.Time,
				}
				view.UpdatedAt = envelope.Time
				views[payload.ChangeSetID] = view
			}
		case TypeChangeSetPublishRequested:
			payload, err := decode[ChangeSetPublishRequested](envelope)
			if err != nil {
				return nil, err
			}
			view, ok := views[payload.ChangeSetID]
			if ok {
				view.State, view.UpdatedAt = ChangeSetPublishing, envelope.Time
				views[payload.ChangeSetID] = view
			}
		}
	}
	flowEnvelopes, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, events.StreamFlows, 0)
	if err != nil {
		return nil, err
	}
	for _, envelope := range flowEnvelopes {
		if envelope.Type != events.TypeFlowVersionPublished {
			continue
		}
		payload, err := decode[events.FlowVersionPublished](envelope)
		if err != nil {
			return nil, err
		}
		if payload.ChangeSetID == "" {
			continue
		}
		view, ok := views[payload.ChangeSetID]
		if !ok {
			continue
		}
		view.State = ChangeSetPublished
		view.PublishedBy, view.PublishedAt = envelope.Actor, envelope.Time
		view.PublishedVersion, view.PublishedEtag = payload.Version, payload.Etag
		view.UpdatedAt = envelope.Time
		views[payload.ChangeSetID] = view
	}
	return views, nil
}

func componentEtag(
	graph events.Graph,
	input ComponentVersionInput,
	dependencies []events.FlowDependency,
) (string, error) {
	source := cloneGraph(input.Graph)
	base, err := domain.EtagWithSource(graph, input.InputSchema, &source, dependencies)
	if err != nil {
		return "", err
	}
	output, err := canonicalJSON(input.OutputSchema)
	if err != nil {
		return "", fmt.Errorf("authoring: output schema: %w", err)
	}
	hash := sha256.Sum256(append([]byte(base+"\x00"), output...))
	return hex.EncodeToString(hash[:]), nil
}

func mutationIdentity(
	id identity.Identity,
	operation, target, key string,
	request any,
) (string, string, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 200 {
		return "", "", errors.New(
			"authoring: idempotency key must contain 1..200 characters",
		)
	}
	document, err := json.Marshal(request)
	if err != nil {
		return "", "", fmt.Errorf("authoring: hash %s request: %w", operation, err)
	}
	keyHash := hashText(
		id.Org + "\x00" + id.Workspace + "\x00" + id.Actor + "\x00" +
			operation + "\x00" + target + "\x00" + key,
	)
	return keyHash, hashText(string(document)), nil
}

func mutationKeyConflict(operation string) error {
	return &IdempotencyConflict{Operation: operation}
}

func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func draftSnapshotEqual(
	leftTitle string,
	leftGraph events.Graph,
	leftSchema json.RawMessage,
	rightTitle string,
	rightGraph events.Graph,
	rightSchema json.RawMessage,
) bool {
	if strings.TrimSpace(leftTitle) != strings.TrimSpace(rightTitle) ||
		!semanticEqual(leftGraph, rightGraph) {
		return false
	}
	leftCanonical, leftErr := canonicalJSON(leftSchema)
	rightCanonical, rightErr := canonicalJSON(rightSchema)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}

func draftRevisionClaim(draftID string, revision int) string {
	return fmt.Sprintf("authoring.draft.revision\x00%s\x00%d", draftID, revision)
}

func componentVersionClaim(componentID string, version int) string {
	return fmt.Sprintf("authoring.component.version\x00%s\x00%d", componentID, version)
}

func normalizeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func hashText(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func randomID() string {
	var raw [16]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		panic("authoring: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(raw[:])
}
