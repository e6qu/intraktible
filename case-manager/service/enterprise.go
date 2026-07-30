// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/case-manager/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
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
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var definition domain.QueueDefinition
	if err := httpx.DecodeJSON(r, &definition); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if definition.Key != r.PathValue("key") {
		httpx.Error(w, http.StatusBadRequest, errPathKeyMismatch)
		return
	}
	event, err := s.cmd.ConfigureQueue(r.Context(), id, definition)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"event_id": event.ID, "seq": event.Seq})
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
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var profile domain.ReviewerProfile
	if err := httpx.DecodeJSON(r, &profile); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if profile.Actor != r.PathValue("actor") {
		httpx.Error(w, http.StatusBadRequest, errPathActorMismatch)
		return
	}
	event, err := s.cmd.ConfigureReviewer(r.Context(), id, profile)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"event_id": event.ID, "seq": event.Seq})
}

func (s *Service) routeCase(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	decision, event, err := s.cmd.RouteCase(r.Context(), id, r.PathValue("case_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"decision": decision, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) routePending(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	routed, failures, err := s.cmd.RoutePending(r.Context(), id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"routed": routed, "count": len(routed), "failures": failures,
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
		request.Disposition, request.ReasonCode, request.Note, request.Override,
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
	attachment.CaseID = r.PathValue("case_id")
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
	event, err := s.cmd.AccessAttachment(
		r.Context(), id, r.PathValue("case_id"), r.PathValue("attachment_id"), request.Purpose,
	)
	writeEvent(w, event, err)
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

func writeEvent(w http.ResponseWriter, event eventlog.Envelope, err error) {
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"event_id": event.ID, "seq": event.Seq})
}

var (
	errInvalidVersion    = &requestError{"case-manager: version must be a positive integer"}
	errPathKeyMismatch   = &requestError{"case-manager: path key must match definition key"}
	errPathActorMismatch = &requestError{"case-manager: path actor must match reviewer actor"}
)

type requestError struct{ message string }

func (e *requestError) Error() string { return e.message }
