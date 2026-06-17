// SPDX-License-Identifier: AGPL-3.0-or-later

// Package service is the Decision Engine's HTTP surface (imperative shell): flow
// management endpoints wiring the command write side and the flows read model.
package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/e6qu/intraktible/decision-engine/analytics"
	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/preapproval"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/store"
)

// Service wires flow commands, the decide runtime, and the read models to HTTP.
type Service struct {
	cmd    *command.Handler
	decide *command.DecideHandler
	pa     *preapproval.Handler
	store  store.Store
}

// New builds the service. The pre-approval handler is shared with the standalone
// pre-approval service so a batch run can promote approved entities into grants.
func New(cmd *command.Handler, decide *command.DecideHandler, pa *preapproval.Handler, st store.Store) *Service {
	return &Service{cmd: cmd, decide: decide, pa: pa, store: st}
}

// Routes registers the flow-management, decide, and decision-history endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/flows", s.create)
	mux.HandleFunc("GET /v1/flows", s.list)
	mux.HandleFunc("GET /v1/flows/{flow_id}", s.get)
	mux.HandleFunc("GET /v1/flows/{flow_id}/metrics", s.metrics)
	mux.HandleFunc("POST /v1/flows/{flow_id}/versions", s.publish)
	mux.HandleFunc("POST /v1/flows/{flow_id}/deployments", s.deploy)
	mux.HandleFunc("POST /v1/flows/{flow_id}/deployment-requests", s.requestDeployment)
	mux.HandleFunc("POST /v1/flows/{flow_id}/deployment-requests/{req_id}/approve", s.approveDeployment)
	mux.HandleFunc("POST /v1/flows/{flow_id}/deployment-requests/{req_id}/reject", s.rejectDeployment)
	mux.HandleFunc("POST /v1/flows/{slug}/{env}/decide", s.runDecide)
	mux.HandleFunc("POST /v1/flows/{slug}/{env}/decide/batch", s.decideBatch)
	mux.HandleFunc("POST /v1/flows/{slug}/{env}/preapprove/batch", s.preapproveBatch)
	mux.HandleFunc("GET /v1/flows/{flow_id}/export", s.exportFlow)
	mux.HandleFunc("POST /v1/flows/{flow_id}/backtest", s.backtestFlow)
	mux.HandleFunc("GET /v1/decisions", s.listDecisions)
	mux.HandleFunc("GET /v1/decisions/{decision_id}", s.getDecision)
	mux.HandleFunc("GET /v1/decisions/{decision_id}/export", s.exportDecision)
}

type createRequest struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	flowID, e, err := s.cmd.CreateFlow(r.Context(), id, domain.CreateFlow{Slug: req.Slug, Name: req.Name})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"flow_id": flowID, "event_id": e.ID, "seq": e.Seq})
}

type publishRequest struct {
	Graph       events.Graph    `json:"graph"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

func (s *Service) publish(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req publishRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	version, etag, e, err := s.cmd.PublishVersion(r.Context(), id, domain.PublishVersion{
		FlowID:      r.PathValue("flow_id"),
		Graph:       req.Graph,
		InputSchema: req.InputSchema,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"version": version, "etag": etag, "event_id": e.ID, "seq": e.Seq,
	})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	fvs, err := flows.List(r.Context(), s.store, id)
	httpx.WriteList(w, "flows", fvs, err)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	fv, found, err := flows.Read(r.Context(), s.store, id, r.PathValue("flow_id"))
	httpx.WriteOne(w, fv, found, err, "flow not found")
}

type deployRequest struct {
	Environment       string `json:"environment"`
	Version           int    `json:"version"`
	ChallengerVersion int    `json:"challenger_version,omitempty"`
	ChallengerPct     int    `json:"challenger_pct,omitempty"`
}

// deploy makes a flow version (and optional A/B challenger) live in an environment.
func (s *Service) deploy(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req deployRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	e, err := s.cmd.Deploy(r.Context(), id, domain.DeployVersion{
		FlowID:            r.PathValue("flow_id"),
		Environment:       req.Environment,
		Version:           req.Version,
		ChallengerVersion: req.ChallengerVersion,
		ChallengerPct:     req.ChallengerPct,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"environment": req.Environment, "version": req.Version, "event_id": e.ID, "seq": e.Seq,
	})
}

// requestDeployment proposes a deployment for review (maker-checker maker side).
func (s *Service) requestDeployment(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req deployRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	reqID, e, err := s.cmd.RequestDeployment(r.Context(), id, domain.DeployVersion{
		FlowID:            r.PathValue("flow_id"),
		Environment:       req.Environment,
		Version:           req.Version,
		ChallengerVersion: req.ChallengerVersion,
		ChallengerPct:     req.ChallengerPct,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"request_id": reqID, "status": "pending", "event_id": e.ID, "seq": e.Seq,
	})
}

// approveDeployment is the checker side: approve a pending request (four-eyes), deploying it.
func (s *Service) approveDeployment(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	e, err := s.cmd.ApproveDeployment(r.Context(), id, r.PathValue("flow_id"), r.PathValue("req_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "approved", "event_id": e.ID, "seq": e.Seq})
}

// rejectDeployment rejects a pending request.
func (s *Service) rejectDeployment(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason,omitempty"`
	}
	_ = httpx.DecodeJSON(r, &req)
	e, err := s.cmd.RejectDeployment(r.Context(), id, r.PathValue("flow_id"), r.PathValue("req_id"), req.Reason)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "rejected", "event_id": e.ID, "seq": e.Seq})
}

type decideRequest struct {
	Data       map[string]any  `json:"data"`
	EntityType string          `json:"entity_type,omitempty"`
	EntityID   string          `json:"entity_id,omitempty"`
	MockData   map[string]any  `json:"mock_data,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	Control    json.RawMessage `json:"control,omitempty"`
}

type decideResponse struct {
	DecisionID  string         `json:"decision_id"`
	Status      string         `json:"status"`
	Data        map[string]any `json:"data,omitempty"`
	Disposition string         `json:"disposition,omitempty"`
	Error       string         `json:"error,omitempty"`
}

// runDecide executes a published flow. A flow whose logic errors is a recorded
// "failed" decision returned with HTTP 200 and status "failed" (the call
// succeeded; the decision outcome did not); only lookup/validation problems 4xx.
func (s *Service) runDecide(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req decideRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.decide.Decide(r.Context(), id, r.PathValue("slug"), r.PathValue("env"), req.Data,
		command.EntityRef{Type: req.EntityType, ID: req.EntityID})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, decideResponse{
		DecisionID: result.DecisionID, Status: result.Status, Data: result.Output,
		Disposition: result.Disposition, Error: result.Error,
	})
}

// maxBatch caps a batch-decide dataset. Unlike backtest (which records nothing),
// every batch row is a real recorded decision, so the cap is conservative.
const maxBatch = 500

type batchRequest struct {
	Dataset    []map[string]any `json:"dataset"`
	EntityType string           `json:"entity_type,omitempty"`
	EntityID   string           `json:"entity_id,omitempty"`
}

type batchResult struct {
	Index       int            `json:"index"`
	DecisionID  string         `json:"decision_id,omitempty"`
	Status      string         `json:"status"` // completed | failed | rejected
	Data        map[string]any `json:"data,omitempty"`
	Disposition string         `json:"disposition,omitempty"`
	Error       string         `json:"error,omitempty"`
}

type batchResponse struct {
	Total     int           `json:"total"`
	Completed int           `json:"completed"`
	Failed    int           `json:"failed"`
	Rejected  int           `json:"rejected"`
	Results   []batchResult `json:"results"`
}

// decideBatch runs a dataset of inputs through the published flow, recording a
// real decision per row (so they appear in history, metrics, and the audit log).
// A row whose input fails validation/lookup is "rejected" (no decision recorded);
// a row whose flow logic errors is a recorded "failed" decision — the batch call
// itself still returns 200 with a per-row breakdown.
func (s *Service) decideBatch(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req batchRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Dataset) == 0 {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("batch: dataset is empty"))
		return
	}
	if len(req.Dataset) > maxBatch {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("batch: dataset too large (%d > %d)", len(req.Dataset), maxBatch))
		return
	}

	slug, env := r.PathValue("slug"), r.PathValue("env")
	ref := command.EntityRef{Type: req.EntityType, ID: req.EntityID}
	resp := batchResponse{Total: len(req.Dataset), Results: make([]batchResult, 0, len(req.Dataset))}
	for i, input := range req.Dataset {
		res, err := s.decide.Decide(r.Context(), id, slug, env, input, ref)
		if err != nil {
			resp.Rejected++
			resp.Results = append(resp.Results, batchResult{Index: i, Status: "rejected", Error: err.Error()})
			continue
		}
		switch res.Status {
		case "completed":
			resp.Completed++
		case "failed":
			resp.Failed++
		}
		resp.Results = append(resp.Results, batchResult{
			Index: i, DecisionID: res.DecisionID, Status: res.Status, Data: res.Output,
			Disposition: res.Disposition, Error: res.Error,
		})
	}
	httpx.JSON(w, http.StatusOK, resp)
}

type preapproveBatchRequest struct {
	Dataset     []map[string]any `json:"dataset"`
	EntityType  string           `json:"entity_type"`
	EntityKey   string           `json:"entity_key"`            // field in each row read as the entity id
	Disposition string           `json:"disposition,omitempty"` // grant rows the policy gave this (default approve)
	ValidDays   int              `json:"valid_days"`
	Note        string           `json:"note,omitempty"`
}

type preapproveResult struct {
	Index         int    `json:"index"`
	EntityID      string `json:"entity_id,omitempty"`
	DecisionID    string `json:"decision_id,omitempty"`
	Status        string `json:"status"` // completed | failed | rejected
	Disposition   string `json:"disposition,omitempty"`
	Granted       bool   `json:"granted"`
	PreApprovalID string `json:"preapproval_id,omitempty"`
	Reason        string `json:"reason,omitempty"` // why a decided row was not granted
	Error         string `json:"error,omitempty"`
}

type preapproveBatchResponse struct {
	Total    int                `json:"total"`
	Granted  int                `json:"granted"`
	Skipped  int                `json:"skipped"`  // decided, but disposition did not match (or grant failed)
	Failed   int                `json:"failed"`   // flow logic errored
	Rejected int                `json:"rejected"` // could not decide (missing entity id / validation)
	Results  []preapproveResult `json:"results"`
}

// preapproveBatch promotes a population into pre-approvals: each row runs through
// the recorded decide path (applying the flow's bound policy), and every row the
// policy disposes to the target disposition (default approve) is granted a
// time-boxed pre-approval keyed by the row's entity id — its output becomes the
// stored terms. This is the bridge from bulk decisioning to durable pre-decisions.
func (s *Service) preapproveBatch(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req preapproveBatchRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	switch {
	case len(req.Dataset) == 0:
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("preapprove batch: dataset is empty"))
		return
	case len(req.Dataset) > maxBatch:
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("preapprove batch: dataset too large (%d > %d)", len(req.Dataset), maxBatch))
		return
	case req.EntityType == "" || req.EntityKey == "":
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("preapprove batch: entity_type and entity_key are required"))
		return
	case req.ValidDays <= 0:
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("preapprove batch: valid_days must be positive"))
		return
	}
	target := req.Disposition
	if target == "" {
		target = preapproval.Approved
	}
	if target != preapproval.Approved && target != preapproval.Declined {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("preapprove batch: disposition must be approve or decline"))
		return
	}

	slug, env := r.PathValue("slug"), r.PathValue("env")
	resp := preapproveBatchResponse{Total: len(req.Dataset), Results: make([]preapproveResult, 0, len(req.Dataset))}
	for i, input := range req.Dataset {
		row := preapproveResult{Index: i, EntityID: stringField(input, req.EntityKey)}
		if row.EntityID == "" {
			row.Status, row.Reason = "rejected", "missing entity id field "+req.EntityKey
			resp.Rejected++
			resp.Results = append(resp.Results, row)
			continue
		}
		res, err := s.decide.Decide(r.Context(), id, slug, env, input,
			command.EntityRef{Type: req.EntityType, ID: row.EntityID})
		if err != nil {
			row.Status, row.Error = "rejected", err.Error()
			resp.Rejected++
			resp.Results = append(resp.Results, row)
			continue
		}
		row.DecisionID, row.Status, row.Disposition = res.DecisionID, res.Status, res.Disposition
		switch {
		case res.Status != domain.StatusCompleted:
			row.Reason = "decision " + res.Status
			resp.Failed++
		case res.Disposition != target:
			row.Reason = "disposition " + dispositionOrNone(res.Disposition)
			resp.Skipped++
		default:
			terms, mErr := json.Marshal(res.Output)
			if mErr != nil {
				row.Reason = "terms: " + mErr.Error()
				resp.Skipped++
				break
			}
			paID, _, gErr := s.pa.Grant(r.Context(), id, preapproval.GrantCmd{
				EntityType: req.EntityType, EntityID: row.EntityID, Disposition: target,
				Terms: terms, FlowSlug: slug, ValidDays: req.ValidDays, Note: req.Note,
			})
			if gErr != nil {
				row.Reason = "grant: " + gErr.Error()
				resp.Skipped++
				break
			}
			row.Granted, row.PreApprovalID = true, paID
			resp.Granted++
		}
		resp.Results = append(resp.Results, row)
	}
	httpx.JSON(w, http.StatusOK, resp)
}

// stringField reads a dataset field as a string id (numbers are formatted without
// scientific notation so an integer-looking id stays stable).
func stringField(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func dispositionOrNone(d string) string {
	if d == "" {
		return "none"
	}
	return d
}

func (s *Service) listDecisions(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	recs, err := history.List(r.Context(), s.store, id)
	httpx.WriteList(w, "decisions", recs, err)
}

func (s *Service) getDecision(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	rec, found, err := history.Read(r.Context(), s.store, id, r.PathValue("decision_id"))
	httpx.WriteOne(w, rec, found, err, "decision not found")
}

func (s *Service) metrics(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	flowID := r.PathValue("flow_id")
	m, found, err := analytics.Read(r.Context(), s.store, id, flowID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		// A flow with no decisions yet has zero metrics, not a 404.
		m = analytics.FlowMetrics{FlowID: flowID, ByEnvironment: map[string]int{}, ByVersion: map[int]int{}, ByVariant: map[string]analytics.VariantStats{}}
	}
	httpx.JSON(w, http.StatusOK, m)
}
