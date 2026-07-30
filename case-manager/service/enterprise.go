// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/command"
	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/case-manager/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
)

func (s *Service) enterpriseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/case-types", s.listCaseTypes)
	mux.HandleFunc("POST /v1/case-types", s.publishCaseType)
	mux.HandleFunc("GET /v1/case-types/{key}/versions/{version}", s.getCaseTypeVersion)
	mux.HandleFunc("GET /v1/case-queues", s.listQueues)
	mux.HandleFunc("PUT /v1/case-queues/{key}", s.configureQueue)
	mux.HandleFunc("GET /v1/case-reviewers", s.listReviewers)
	mux.HandleFunc("PUT /v1/case-reviewers/{actor}", s.configureReviewer)
	mux.HandleFunc("POST /v1/cases/{case_id}/route", s.routeCase)
	mux.HandleFunc("POST /v1/cases/route-pending", s.routePending)
	mux.HandleFunc("POST /v1/cases/{case_id}/priority", s.setPriority)
	mux.HandleFunc("PATCH /v1/cases/{case_id}/fields", s.updateFields)
	mux.HandleFunc("POST /v1/cases/{case_id}/disposition", s.recordDisposition)
	mux.HandleFunc("POST /v1/cases/{case_id}/evidence", s.linkEvidence)
	mux.HandleFunc("POST /v1/cases/{case_id}/attachments", s.registerAttachment)
	mux.HandleFunc("POST /v1/cases/{case_id}/attachments/{attachment_id}/access", s.accessAttachment)
	mux.HandleFunc("POST /v1/cases/{case_id}/qa/select", s.selectQA)
	mux.HandleFunc("POST /v1/cases/{case_id}/qa/review", s.reviewQA)
	mux.HandleFunc("POST /v1/cases/{case_id}/qa/feedback", s.qaFeedback)
	mux.HandleFunc("GET /v1/case-views", s.listSavedViews)
	mux.HandleFunc("POST /v1/case-views", s.saveView)
	mux.HandleFunc("DELETE /v1/case-views/{view_id}", s.deleteView)
	mux.HandleFunc("POST /v1/cases/bulk", s.bulkCases)
	mux.HandleFunc("GET /v1/case-bulk", s.listBulk)
	mux.HandleFunc("GET /v1/case-bulk/{batch_id}", s.getBulk)
	mux.HandleFunc("GET /v1/case-validated-outcomes", s.listValidatedOutcomes)
	mux.HandleFunc("GET /v1/cases/duplicates", s.duplicates)
	mux.HandleFunc("POST /v1/cases/rebalance", s.rebalance)
	mux.HandleFunc("GET /v1/cases/analytics", s.analytics)
	mux.HandleFunc("GET /v1/cases/export", s.exportCases)
	mux.HandleFunc("POST /v1/cases/{case_id}/webhook/retry", s.retrySLAWebhook)
}

func (s *Service) updateFields(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		Fields json.RawMessage `json:"fields"`
	}
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	event, err := s.cmd.UpdateFields(
		r.Context(), id, r.PathValue("case_id"), request.Fields,
		string(httpx.RoleOf(r.Context())),
	)
	writeEvent(w, event, err)
}

func (s *Service) listValidatedOutcomes(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	outcomes, err := cases.ListValidatedOutcomes(r.Context(), s.store, id)
	httpx.WriteList(w, "validated_outcomes", outcomes, err)
}

func (s *Service) listCaseTypes(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	views, err := cases.LatestCaseTypes(r.Context(), s.store, id)
	httpx.WriteList(w, "case_types", views, err)
}

func (s *Service) publishCaseType(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var definition domain.CaseTypeDefinition
	if err := httpx.DecodeJSON(r, &definition); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	version, event, err := s.cmd.PublishCaseType(r.Context(), id, definition)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"key": definition.Key, "version": version, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) getCaseTypeVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || version < 1 {
		httpx.Error(w, http.StatusBadRequest, errInvalidVersion)
		return
	}
	view, found, err := cases.CaseTypeVersion(r.Context(), s.store, id, r.PathValue("key"), version)
	httpx.WriteOne(w, view, found, err, "case type version not found")
}

func (s *Service) listQueues(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	views, err := cases.ListQueues(r.Context(), s.store, id)
	httpx.WriteList(w, "queues", views, err)
}

func (s *Service) configureQueue(w http.ResponseWriter, r *http.Request) {
	var definition domain.QueueDefinition
	httpx.Emit(w, r, &definition, func(id identity.Identity) (eventlog.Envelope, error) {
		if definition.Key != r.PathValue("key") {
			return eventlog.Envelope{}, errPathKeyMismatch
		}
		return s.cmd.ConfigureQueue(r.Context(), id, definition)
	})
}

func (s *Service) listReviewers(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	views, err := cases.ListReviewers(r.Context(), s.store, id)
	httpx.WriteList(w, "reviewers", views, err)
}

func (s *Service) configureReviewer(w http.ResponseWriter, r *http.Request) {
	var profile domain.ReviewerProfile
	httpx.Emit(w, r, &profile, func(id identity.Identity) (eventlog.Envelope, error) {
		if profile.Actor != r.PathValue("actor") {
			return eventlog.Envelope{}, errPathActorMismatch
		}
		return s.cmd.ConfigureReviewer(r.Context(), id, profile)
	})
}

func (s *Service) routeCase(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	decision, event, err := s.cmd.RouteCase(r.Context(), id, r.PathValue("case_id"))
	writeRoutingDecision(w, decision, event, err)
}

func writeRoutingDecision(
	w http.ResponseWriter,
	decision domain.RoutingDecision,
	event eventlog.Envelope,
	err error,
) {
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"decision": decision, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) routePending(w http.ResponseWriter, r *http.Request) {
	s.routingBatch(w, r, "routed", http.StatusInternalServerError, s.cmd.RoutePending)
}

func (s *Service) routingBatch(
	w http.ResponseWriter,
	r *http.Request,
	resultKey string,
	errorStatus int,
	run func(context.Context, identity.Identity) (map[string]string, []string, error),
) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	results, failures, err := run(r.Context(), id)
	if err != nil {
		httpx.Error(w, errorStatus, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		resultKey: results, "count": len(results), "failures": failures,
	})
}

func (s *Service) setPriority(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		Priority domain.Priority `json:"priority"`
	}
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	event, err := s.cmd.SetPriority(r.Context(), id, r.PathValue("case_id"), request.Priority)
	writeEvent(w, event, err)
}

func (s *Service) recordDisposition(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		Disposition string `json:"disposition"`
		ReasonCode  string `json:"reason_code"`
		Note        string `json:"note,omitempty"`
		Override    bool   `json:"override,omitempty"`
	}
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	event, err := s.cmd.RecordDisposition(
		r.Context(), id, r.PathValue("case_id"),
		request.Disposition, request.ReasonCode, request.Note,
		string(httpx.RoleOf(r.Context())), request.Override,
	)
	writeEvent(w, event, err)
}

func (s *Service) linkEvidence(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var link events.CaseEvidenceLinked
	if err := httpx.DecodeJSON(r, &link); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	link.CaseID = r.PathValue("case_id")
	event, err := s.cmd.LinkEvidence(r.Context(), id, link)
	writeEvent(w, event, err)
}

func (s *Service) registerAttachment(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var attachment events.CaseAttachmentRegistered
	if err := httpx.DecodeJSON(r, &attachment); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if attachment.RetainUntil != "" || attachment.LegalHold {
		httpx.Error(w, http.StatusBadRequest, errAttachmentGovernanceOwned)
		return
	}
	attachment.CaseID = r.PathValue("case_id")
	view, found, err := cases.Read(r.Context(), s.store, id, attachment.CaseID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, errCaseNotFound)
		return
	}
	if attachment.Subject == "" {
		attachment.Subject = view.Subject
	}
	if s.governance != nil && attachment.Subject != "" {
		status, err := s.governance.Status(r.Context(), id, attachment.Subject, s.now())
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err)
			return
		}
		if status.Erased {
			httpx.Error(w, http.StatusConflict, errAttachmentSubjectErased)
			return
		}
		attachment.LegalHold = status.LegalHold
		if status.Retained {
			attachment.RetainUntil = status.RetainUntil.Format(time.RFC3339)
		}
	}
	event, err := s.cmd.RegisterAttachment(r.Context(), id, attachment)
	writeEvent(w, event, err)
}

func (s *Service) accessAttachment(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		Purpose string `json:"purpose"`
	}
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	view, found, err := cases.Read(r.Context(), s.store, id, r.PathValue("case_id"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, errCaseNotFound)
		return
	}
	storageRef := ""
	for _, attachment := range view.Attachments {
		if attachment.AttachmentID != r.PathValue("attachment_id") {
			continue
		}
		storageRef = attachment.StorageRef
		if s.governance != nil && attachment.Subject != "" {
			status, err := s.governance.Status(r.Context(), id, attachment.Subject, s.now())
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, err)
				return
			}
			if status.Erased {
				httpx.Error(w, http.StatusGone, errAttachmentSubjectErased)
				return
			}
		}
		break
	}
	if storageRef == "" {
		httpx.Error(w, http.StatusNotFound, errAttachmentNotFound)
		return
	}
	event, err := s.cmd.AccessAttachment(
		r.Context(), id, r.PathValue("case_id"), r.PathValue("attachment_id"), request.Purpose,
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"event_id": event.ID, "seq": event.Seq, "storage_ref": storageRef,
	})
}

func (s *Service) selectQA(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		SampleID string `json:"sample_id"`
		Reviewer string `json:"reviewer"`
		RateBPS  int    `json:"rate_bps"`
	}
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	selected, event, err := s.cmd.SelectQA(
		r.Context(), id, r.PathValue("case_id"), request.SampleID, request.Reviewer, request.RateBPS,
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"selected": selected, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) reviewQA(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		SampleID    string `json:"sample_id"`
		Disposition string `json:"disposition"`
		ReasonCode  string `json:"reason_code"`
		Note        string `json:"note,omitempty"`
		Override    bool   `json:"override,omitempty"`
	}
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	event, err := s.cmd.ReviewQA(
		r.Context(), id, r.PathValue("case_id"), request.SampleID,
		request.Disposition, request.ReasonCode, request.Note, request.Override,
	)
	writeEvent(w, event, err)
}

func (s *Service) qaFeedback(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		SampleID string `json:"sample_id"`
		Text     string `json:"text"`
	}
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	event, err := s.cmd.AddReviewerFeedback(
		r.Context(), id, r.PathValue("case_id"), request.SampleID, request.Text,
	)
	writeEvent(w, event, err)
}

func (s *Service) listSavedViews(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	views, err := cases.ListSavedViews(r.Context(), s.store, id)
	httpx.WriteList(w, "views", views, err)
}

func (s *Service) saveView(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		ViewID string          `json:"view_id,omitempty"`
		Name   string          `json:"name"`
		Query  json.RawMessage `json:"query"`
	}
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	viewID, event, err := s.cmd.SaveView(r.Context(), id, request.ViewID, request.Name, request.Query)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"view_id": viewID, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) deleteView(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	event, err := s.cmd.DeleteView(r.Context(), id, r.PathValue("view_id"))
	writeEvent(w, event, err)
}

func (s *Service) bulkCases(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request command.BulkRequest
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	request.Role = string(httpx.RoleOf(r.Context()))
	result, err := s.cmd.Bulk(r.Context(), id, r.Header.Get("Idempotency-Key"), request)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

func (s *Service) listBulk(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	views, err := cases.ListBulk(r.Context(), s.store, id)
	httpx.WriteList(w, "bulk_operations", views, err)
}

func (s *Service) getBulk(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	view, found, err := cases.ReadBulk(r.Context(), s.store, id, r.PathValue("batch_id"))
	httpx.WriteOne(w, view, found, err, "bulk operation not found")
}

func (s *Service) duplicates(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	views, err := cases.List(r.Context(), s.store, id, cases.Filter{})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"duplicate_groups": cases.FindDuplicates(views)})
}

func (s *Service) rebalance(w http.ResponseWriter, r *http.Request) {
	s.routingBatch(w, r, "moved", http.StatusBadRequest, s.cmd.Rebalance)
}

func (s *Service) analytics(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	result, err := cases.Analytics(r.Context(), s.store, id, s.now())
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

func (s *Service) retrySLAWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	event, err := s.cmd.RetrySLAEscalation(r.Context(), id, r.PathValue("case_id"), request.Reason)
	writeEvent(w, event, err)
}

func (s *Service) exportCases(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	views, err := cases.List(r.Context(), s.store, id, s.filterFrom(r))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	for i := range views {
		cases.AnnotateSLA(&views[i], s.now())
		if err := s.annotateRead(r.Context(), id, string(httpx.RoleOf(r.Context())), &views[i]); err != nil {
			httpx.Error(w, http.StatusInternalServerError, err)
			return
		}
	}
	data, contentType, extension, err := cases.ExportAudit(views, r.URL.Query().Get("format"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.Download(w, contentType, "case-audit."+extension, string(data))
}

func writeEvent(w http.ResponseWriter, event eventlog.Envelope, err error) {
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"event_id": event.ID, "seq": event.Seq})
}

var (
	errInvalidVersion            = &requestError{"case-manager: version must be a positive integer"}
	errPathKeyMismatch           = &requestError{"case-manager: path key must match definition key"}
	errPathActorMismatch         = &requestError{"case-manager: path actor must match reviewer actor"}
	errCaseNotFound              = &requestError{"case-manager: case not found"}
	errAttachmentGovernanceOwned = &requestError{"case-manager: retain_until and legal_hold are server-owned governance state"}
	errAttachmentSubjectErased   = &requestError{"case-manager: attachment subject has been erased"}
	errAttachmentNotFound        = &requestError{"case-manager: attachment not found"}
)

type requestError struct{ message string }

func (e *requestError) Error() string { return e.message }
