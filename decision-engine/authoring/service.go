// SPDX-License-Identifier: AGPL-3.0-or-later

package authoring

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/grants"
	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/privacy"
	"github.com/e6qu/intraktible/platform/store"
)

// Service exposes collaborative authoring and reusable assets.
type Service struct {
	handler *Handler
	store   store.Store
}

func New(handler *Handler, st store.Store) *Service {
	return &Service{handler: handler, store: st}
}

func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/authoring/import", s.importCanonicalFlow)
	mux.HandleFunc("POST /v1/authoring/import-bundle", s.importCanonicalBundle)
	mux.HandleFunc("POST /v1/authoring/drafts", s.createDraft)
	mux.HandleFunc("GET /v1/authoring/drafts", s.listDrafts)
	mux.HandleFunc("GET /v1/authoring/drafts/{draft_id}", s.getDraft)
	mux.HandleFunc("PUT /v1/authoring/drafts/{draft_id}", s.saveDraft)
	mux.HandleFunc("POST /v1/authoring/drafts/{draft_id}/rebase", s.rebaseDraft)
	mux.HandleFunc("GET /v1/authoring/drafts/{draft_id}/revisions", s.listDraftRevisions)
	mux.HandleFunc("GET /v1/authoring/drafts/{draft_id}/export", s.exportCanonicalDraft)
	mux.HandleFunc("DELETE /v1/authoring/drafts/{draft_id}", s.archiveDraft)
	mux.HandleFunc("GET /v1/authoring/drafts/{draft_id}/presence", s.listPresence)
	mux.HandleFunc("PUT /v1/authoring/drafts/{draft_id}/presence", s.renewPresence)
	mux.HandleFunc("DELETE /v1/authoring/drafts/{draft_id}/presence", s.leavePresence)

	mux.HandleFunc("POST /v1/authoring/components", s.createComponent)
	mux.HandleFunc("GET /v1/authoring/components", s.listComponents)
	mux.HandleFunc("GET /v1/authoring/components/{component_id}", s.getComponent)
	mux.HandleFunc("POST /v1/authoring/components/{component_id}/versions", s.publishComponent)
	mux.HandleFunc("GET /v1/authoring/components/{component_id}/compatibility", s.componentCompatibility)
	mux.HandleFunc("POST /v1/authoring/components/{component_id}/upgrade-drafts", s.createComponentUpgradeDrafts)
	mux.HandleFunc("DELETE /v1/authoring/components/{component_id}", s.retireComponent)
	mux.HandleFunc("GET /v1/authoring/components/{component_id}/versions/{version}/consumers", s.componentConsumers)

	mux.HandleFunc("POST /v1/authoring/changesets", s.createChangeSet)
	mux.HandleFunc("GET /v1/authoring/changesets", s.listChangeSets)
	mux.HandleFunc("GET /v1/authoring/changesets/{changeset_id}", s.getChangeSet)
	mux.HandleFunc("GET /v1/authoring/changesets/{changeset_id}/diff", s.changeSetDiff)
	mux.HandleFunc("POST /v1/authoring/changesets/{changeset_id}/checks", s.recordCheck)
	mux.HandleFunc("POST /v1/authoring/changesets/{changeset_id}/submit", s.submitChangeSet)
	mux.HandleFunc("POST /v1/authoring/changesets/{changeset_id}/review", s.reviewChangeSet)
	mux.HandleFunc("POST /v1/authoring/changesets/{changeset_id}/publish", s.publishChangeSet)
}

func (s *Service) importCanonicalFlow(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var asset CanonicalFlow
	if err := httpx.DecodeJSON(r, &asset); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.handler.ImportCanonicalFlow(
		r.Context(), id, asset, r.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeAuthoringError(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"flow_id": result.FlowID, "draft_id": result.DraftID,
		"revision": result.Revision, "created": result.Created,
		"migration_report": result.MigrationReport,
		"event_id":         result.Event.ID, "seq": result.Event.Seq,
	})
}

func (s *Service) importCanonicalBundle(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var bundle CanonicalBundle
	if err := httpx.DecodeJSON(r, &bundle); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if bundle.FormatVersion != CanonicalFormatV1 || bundle.Kind != "bundle" {
		httpx.Error(
			w, http.StatusBadRequest,
			fmt.Errorf(
				"authoring: unsupported canonical bundle %q kind %q",
				bundle.FormatVersion, bundle.Kind,
			),
		)
		return
	}
	if len(bundle.Flows) == 0 || len(bundle.Flows) > 200 {
		httpx.Error(
			w, http.StatusBadRequest,
			errors.New("authoring: canonical bundle must contain 1..200 flows"),
		)
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 180 {
		httpx.Error(
			w, http.StatusBadRequest,
			errors.New("authoring: idempotency key must contain 1..180 characters for a bundle"),
		)
		return
	}
	seen := make(map[string]bool, len(bundle.Flows))
	for _, asset := range bundle.Flows {
		if _, err := NormalizeCanonicalFlow(asset); err != nil {
			httpx.Error(w, http.StatusBadRequest, err)
			return
		}
		if seen[asset.Slug] {
			httpx.Error(
				w, http.StatusBadRequest,
				fmt.Errorf("authoring: duplicate bundle flow slug %q", asset.Slug),
			)
			return
		}
		seen[asset.Slug] = true
	}
	for _, asset := range bundle.Flows {
		if err := s.handler.ValidateCanonicalImport(
			r.Context(), id, asset, key+"/"+asset.Slug,
		); err != nil {
			writeAuthoringError(w, err)
			return
		}
	}
	results := make([]ImportResult, 0, len(bundle.Flows))
	var maxSeq uint64
	for _, asset := range bundle.Flows {
		result, err := s.handler.ImportCanonicalFlow(
			r.Context(), id, asset, key+"/"+asset.Slug,
		)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err)
			return
		}
		results = append(results, result)
		if result.Event.Seq > maxSeq {
			maxSeq = result.Event.Seq
		}
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"imports": results, "seq": maxSeq,
	})
}

func (s *Service) exportCanonicalDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	draft, found, err := ReadDraft(
		r.Context(), s.store, id, r.PathValue("draft_id"),
	)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, errors.New("authoring draft not found"))
		return
	}
	flow, found, err := flows.Read(r.Context(), s.store, id, draft.FlowID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusConflict, errors.New("authoring draft flow is missing"))
		return
	}
	sensitiveFields, err := privacy.Fields(r.Context(), s.store, id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if err := rejectSensitiveFixtures(draft.Graph, sensitiveFields); err != nil {
		httpx.Error(w, http.StatusConflict, err)
		return
	}
	components, err := ListComponents(r.Context(), s.store, id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	slugForID := make(map[string]string, len(components))
	for _, component := range components {
		slugForID[component.ComponentID] = component.Slug
	}
	canonicalGraph, err := canonicalComponentReferences(draft.Graph, slugForID)
	if err != nil {
		httpx.Error(w, http.StatusConflict, err)
		return
	}
	document, err := MarshalCanonicalFlow(CanonicalFlow{
		FormatVersion: CanonicalFormatV1, Kind: CanonicalKindFlow,
		Slug: flow.Slug, Name: flow.Name, Description: flow.Description,
		Graph: canonicalGraph, InputSchema: draft.InputSchema,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.handler.RecordDraftExport(
		r.Context(), id, draft.DraftID, draft.FlowID, draft.Revision,
	); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(
		"Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s.intraktible.json"`, flow.Slug),
	)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(document)
}

func (s *Service) createDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var input DraftInput
	if err := httpx.DecodeJSON(r, &input); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if !s.allowFlowAny(w, r, id, input.FlowID) {
		return
	}
	draftID, event, err := s.handler.CreateDraft(
		r.Context(), id, input, r.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeAuthoringError(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"draft_id": draftID, "revision": 1, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) listDrafts(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	items, err := ListDrafts(r.Context(), s.store, id, strings.TrimSpace(r.URL.Query().Get("flow_id")))
	httpx.WriteList(w, "drafts", items, err)
}

func (s *Service) getDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	view, found, err := ReadDraft(r.Context(), s.store, id, r.PathValue("draft_id"))
	httpx.WriteOne(w, view, found, err, "authoring draft not found")
}

func (s *Service) saveDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var input SaveDraftInput
	if err := httpx.DecodeJSON(r, &input); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if _, ok := s.draftForMutation(w, r, id, r.PathValue("draft_id")); !ok {
		return
	}
	revision, event, err := s.handler.SaveDraft(r.Context(), id, r.PathValue("draft_id"), input)
	if err != nil {
		var conflict *RevisionConflict
		if errors.As(err, &conflict) {
			httpx.JSON(w, http.StatusConflict, map[string]any{
				"error": err.Error(), "current": conflict.Current,
			})
			return
		}
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"draft_id": r.PathValue("draft_id"), "revision": revision,
		"event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) archiveDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	if _, ok := s.draftForMutation(w, r, id, r.PathValue("draft_id")); !ok {
		return
	}
	event, err := s.handler.ArchiveDraft(r.Context(), id, r.PathValue("draft_id"))
	writeEvent(w, http.StatusOK, event.ID, event.Seq, err)
}

func (s *Service) rebaseDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var input RebaseDraftInput
	if err := httpx.DecodeJSON(r, &input); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if _, ok := s.draftForMutation(w, r, id, r.PathValue("draft_id")); !ok {
		return
	}
	revision, event, err := s.handler.RebaseDraft(
		r.Context(), id, r.PathValue("draft_id"), input,
	)
	if err != nil {
		writeAuthoringError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"draft_id": r.PathValue("draft_id"), "revision": revision,
		"event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) listDraftRevisions(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	if _, found, err := ReadDraft(
		r.Context(), s.store, id, r.PathValue("draft_id"),
	); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	} else if !found {
		httpx.Error(w, http.StatusNotFound, errors.New("authoring draft not found"))
		return
	}
	items, err := ListDraftRevisions(
		r.Context(), s.store, id, r.PathValue("draft_id"),
	)
	httpx.WriteList(w, "revisions", items, err)
}

func (s *Service) renewPresence(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		DisplayName string `json:"display_name,omitempty"`
		Revision    int    `json:"revision"`
		SelectedID  string `json:"selected_id,omitempty"`
		TTLSeconds  int    `json:"ttl_seconds,omitempty"`
	}
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if request.TTLSeconds == 0 {
		request.TTLSeconds = 45
	}
	if _, ok := s.draftForMutation(w, r, id, r.PathValue("draft_id")); !ok {
		return
	}
	presence, err := s.handler.RenewPresence(
		r.Context(), id, r.PathValue("draft_id"),
		request.DisplayName, request.Revision, request.SelectedID,
		time.Duration(request.TTLSeconds)*time.Second,
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, presence)
}

func (s *Service) listPresence(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	items, err := s.handler.ListPresence(r.Context(), id, r.PathValue("draft_id"))
	httpx.WriteList(w, "presence", items, err)
}

func (s *Service) leavePresence(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	if _, ok := s.draftForMutation(w, r, id, r.PathValue("draft_id")); !ok {
		return
	}
	if err := s.handler.LeavePresence(r.Context(), id, r.PathValue("draft_id")); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) createComponent(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var input ComponentInput
	if err := httpx.DecodeJSON(r, &input); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	componentID, event, err := s.handler.CreateComponent(
		r.Context(), id, input, r.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeAuthoringError(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"component_id": componentID, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) listComponents(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	items, err := ListComponents(r.Context(), s.store, id)
	httpx.WriteList(w, "components", items, err)
}

func (s *Service) getComponent(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	view, found, err := ReadComponent(r.Context(), s.store, id, r.PathValue("component_id"))
	httpx.WriteOne(w, view, found, err, "authoring component not found")
}

func (s *Service) publishComponent(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var input ComponentVersionInput
	if err := httpx.DecodeJSON(r, &input); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	version, etag, event, err := s.handler.PublishComponent(
		r.Context(), id, r.PathValue("component_id"), input,
		r.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeAuthoringError(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"version": version, "etag": etag, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) retireComponent(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	event, err := s.handler.RetireComponent(r.Context(), id, r.PathValue("component_id"))
	writeEvent(w, http.StatusOK, event.ID, event.Seq, err)
}

func (s *Service) componentConsumers(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || version < 1 {
		httpx.Error(w, http.StatusBadRequest, errors.New("version must be a positive integer"))
		return
	}
	items, err := ListConsumers(
		r.Context(), s.store, id, r.PathValue("component_id"), version,
	)
	httpx.WriteList(w, "consumers", items, err)
}

func (s *Service) componentCompatibility(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	from, fromErr := strconv.Atoi(r.URL.Query().Get("from_version"))
	to, toErr := strconv.Atoi(r.URL.Query().Get("to_version"))
	if fromErr != nil || toErr != nil || from < 1 || to < 1 || from == to {
		httpx.Error(
			w, http.StatusBadRequest,
			errors.New("from_version and to_version must be different positive integers"),
		)
		return
	}
	component, found, err := ReadComponent(
		r.Context(), s.store, id, r.PathValue("component_id"),
	)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, errors.New("authoring component not found"))
		return
	}
	if from > len(component.Versions) || to > len(component.Versions) {
		httpx.Error(w, http.StatusNotFound, errors.New("authoring component version not found"))
		return
	}
	oldVersion, newVersion := component.Versions[from-1], component.Versions[to-1]
	report, err := AssessComponentCompatibility(
		from, to,
		oldVersion.InputSchema, oldVersion.OutputSchema,
		newVersion.InputSchema, newVersion.OutputSchema,
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	consumers, err := ListConsumers(
		r.Context(), s.store, id, component.ComponentID, from,
	)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"component_id": component.ComponentID,
		"report":       report,
		"consumers":    consumers,
		"upgradeable":  report.Status == CompatibilityCompatible,
	})
}

func (s *Service) createComponentUpgradeDrafts(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var input ComponentUpgradeInput
	if err := httpx.DecodeJSON(r, &input); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	for _, flowID := range input.FlowIDs {
		if !s.allowFlowAny(w, r, id, flowID) {
			return
		}
	}
	results, err := s.handler.CreateComponentUpgradeDrafts(
		r.Context(), id, r.PathValue("component_id"), input,
		r.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeAuthoringError(w, err)
		return
	}
	var maxSeq uint64
	for _, result := range results {
		if result.Event.Seq > maxSeq {
			maxSeq = result.Event.Seq
		}
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"drafts": results,
		"seq":    maxSeq,
	})
}

func (s *Service) createChangeSet(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var input ChangeSetInput
	if err := httpx.DecodeJSON(r, &input); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if _, ok := s.draftForMutation(w, r, id, input.DraftID); !ok {
		return
	}
	changeSetID, event, err := s.handler.CreateChangeSet(
		r.Context(), id, input, r.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeAuthoringError(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"changeset_id": changeSetID, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) listChangeSets(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	items, err := ListChangeSets(
		r.Context(), s.store, id, strings.TrimSpace(r.URL.Query().Get("flow_id")),
	)
	httpx.WriteList(w, "changesets", items, err)
}

func (s *Service) getChangeSet(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	view, found, err := ReadChangeSet(
		r.Context(), s.store, id, r.PathValue("changeset_id"),
	)
	httpx.WriteOne(w, view, found, err, "authoring changeset not found")
}

func (s *Service) changeSetDiff(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	differences, err := s.handler.DifferenceForChangeSet(
		r.Context(), id, r.PathValue("changeset_id"),
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"changeset_id": r.PathValue("changeset_id"), "differences": differences,
	})
}

func (s *Service) recordCheck(w http.ResponseWriter, r *http.Request) {
	decodeChangeSetMutation(s, w, r, s.handler.RecordCheck)
}

func (s *Service) submitChangeSet(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	if _, ok := s.changeSetForMutation(w, r, id, r.PathValue("changeset_id")); !ok {
		return
	}
	event, err := s.handler.SubmitChangeSet(
		r.Context(), id, r.PathValue("changeset_id"),
	)
	writeEvent(w, http.StatusOK, event.ID, event.Seq, err)
}

func (s *Service) reviewChangeSet(w http.ResponseWriter, r *http.Request) {
	decodeChangeSetMutation(s, w, r, s.handler.ReviewChangeSet)
}

func decodeChangeSetMutation[T any](
	s *Service,
	w http.ResponseWriter,
	r *http.Request,
	mutate func(
		context.Context, identity.Identity, string, T,
	) (eventlog.Envelope, error),
) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	if _, ok := s.changeSetForMutation(w, r, id, r.PathValue("changeset_id")); !ok {
		return
	}
	var input T
	if err := httpx.DecodeJSON(r, &input); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	event, err := mutate(
		r.Context(), id, r.PathValue("changeset_id"), input,
	)
	writeEvent(w, http.StatusOK, event.ID, event.Seq, err)
}

func (s *Service) publishChangeSet(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	if _, ok := s.changeSetForMutation(w, r, id, r.PathValue("changeset_id")); !ok {
		return
	}
	version, etag, event, err := s.handler.PublishChangeSet(
		r.Context(), id, r.PathValue("changeset_id"),
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	body := map[string]any{"version": version, "etag": etag}
	if event.ID != "" {
		body["event_id"], body["seq"] = event.ID, event.Seq
	}
	httpx.JSON(w, http.StatusCreated, body)
}

func (s *Service) allowFlowAny(
	w http.ResponseWriter,
	r *http.Request,
	id identity.Identity,
	flowID string,
) bool {
	if httpx.RoleOf(r.Context()) != auth.RoleAdmin {
		allowed, err := grants.AllowedAny(r.Context(), s.store, id, flowID, id.Actor)
		switch {
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, err)
			return false
		case !allowed:
			httpx.Error(
				w, http.StatusForbidden,
				fmt.Errorf("requires a per-flow grant for %q", flowID),
			)
			return false
		}
	}
	return true
}

func (s *Service) draftForMutation(
	w http.ResponseWriter,
	r *http.Request,
	id identity.Identity,
	draftID string,
) (DraftView, bool) {
	return readMutationTarget(
		s, w, r, id, draftID, ReadDraft,
		func(draft DraftView) string { return draft.FlowID },
		"authoring draft not found",
	)
}

func (s *Service) changeSetForMutation(
	w http.ResponseWriter,
	r *http.Request,
	id identity.Identity,
	changeSetID string,
) (ChangeSetView, bool) {
	return readMutationTarget(
		s, w, r, id, changeSetID, ReadChangeSet,
		func(changeSet ChangeSetView) string { return changeSet.FlowID },
		"authoring changeset not found",
	)
}

func readMutationTarget[T any](
	s *Service,
	w http.ResponseWriter,
	r *http.Request,
	id identity.Identity,
	targetID string,
	read func(context.Context, store.Store, identity.Identity, string) (T, bool, error),
	flowID func(T) string,
	missing string,
) (T, bool) {
	target, found, err := read(r.Context(), s.store, id, targetID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return target, false
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, errors.New(missing))
		return target, false
	}
	if !s.allowFlowAny(w, r, id, flowID(target)) {
		return target, false
	}
	return target, true
}

func writeEvent(
	w http.ResponseWriter,
	status int,
	eventID string,
	seq uint64,
	err error,
) {
	if err != nil {
		writeAuthoringError(w, err)
		return
	}
	httpx.JSON(w, status, map[string]any{"event_id": eventID, "seq": seq})
}

func writeAuthoringError(w http.ResponseWriter, err error) {
	var conflict *RevisionConflict
	if errors.As(err, &conflict) {
		httpx.JSON(w, http.StatusConflict, map[string]any{
			"error": err.Error(), "current": conflict.Current,
		})
		return
	}
	var compatibility *CompatibilityError
	if errors.As(err, &compatibility) {
		httpx.JSON(w, http.StatusConflict, map[string]any{
			"error": err.Error(), "compatibility": compatibility.Report,
		})
		return
	}
	var idempotency *IdempotencyConflict
	if errors.As(err, &idempotency) {
		httpx.Error(w, http.StatusConflict, err)
		return
	}
	httpx.Error(w, http.StatusBadRequest, err)
}
