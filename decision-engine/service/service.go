// SPDX-License-Identifier: AGPL-3.0-or-later

// Package service is the Decision Engine's HTTP surface (imperative shell): flow
// management endpoints wiring the command write side and the flows read model.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/intraktible/decision-engine/analytics"
	"github.com/e6qu/intraktible/decision-engine/assertions"
	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/monitor"
	"github.com/e6qu/intraktible/decision-engine/preapproval"
	"github.com/e6qu/intraktible/decision-engine/shadow"
	"github.com/e6qu/intraktible/platform/erasure"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/privacy"
	"github.com/e6qu/intraktible/platform/store"
)

// Service wires flow commands, the decide runtime, and the read models to HTTP.
type Service struct {
	cmd    *command.Handler
	decide *command.DecideHandler
	pa     *preapproval.Handler
	store  store.Store
	eraser *erasure.Vault
}

// New builds the service. The pre-approval handler is shared with the standalone
// pre-approval service so a batch run can promote approved entities into grants.
func New(cmd *command.Handler, decide *command.DecideHandler, pa *preapproval.Handler, st store.Store) *Service {
	return &Service{cmd: cmd, decide: decide, pa: pa, store: st}
}

// UseEraser enables unsealing of a decision record's crypto-shredded PII fields
// at the read boundary (sealed at decide time under the entity subject; shown
// "[erased]" once the subject is erased).
func (s *Service) UseEraser(v *erasure.Vault) { s.eraser = v }

// Routes registers the flow-management, decide, and decision-history endpoints.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/flows", s.create)
	mux.HandleFunc("POST /v1/flows/import", s.importFlow)
	mux.HandleFunc("POST /v1/flows/import-bundle", s.importBundle)
	mux.HandleFunc("GET /v1/flows", s.list)
	mux.HandleFunc("GET /v1/flows/{flow_id}", s.get)
	mux.HandleFunc("GET /v1/flows/{flow_id}/metrics", s.metrics)
	mux.HandleFunc("POST /v1/flows/{flow_id}/versions", s.publish)
	mux.HandleFunc("POST /v1/flows/{flow_id}/deployments", s.deploy)
	mux.HandleFunc("POST /v1/flows/{flow_id}/promote", s.promote)
	mux.HandleFunc("GET /v1/flows/{flow_id}/promotion-policy", s.getPromotionPolicy)
	mux.HandleFunc("PUT /v1/flows/{flow_id}/promotion-policy", s.setPromotionPolicy)
	mux.HandleFunc("GET /v1/flows/{flow_id}/shadow", s.getShadow)
	mux.HandleFunc("PUT /v1/flows/{flow_id}/shadow", s.setShadow)
	mux.HandleFunc("POST /v1/flows/{flow_id}/deployment-requests", s.requestDeployment)
	mux.HandleFunc("POST /v1/flows/{flow_id}/deployment-requests/{req_id}/approve", s.approveDeployment)
	mux.HandleFunc("POST /v1/flows/{flow_id}/deployment-requests/{req_id}/reject", s.rejectDeployment)
	mux.HandleFunc("POST /v1/flows/{slug}/{env}/decide", s.runDecide)
	mux.HandleFunc("POST /v1/flows/{slug}/{env}/decide/batch", s.decideBatch)
	mux.HandleFunc("POST /v1/flows/{slug}/{env}/preapprove/batch", s.preapproveBatch)
	mux.HandleFunc("GET /v1/flows/{flow_id}/export", s.exportFlow)
	mux.HandleFunc("POST /v1/flows/{flow_id}/backtest", s.backtestFlow)
	mux.HandleFunc("POST /v1/flows/{flow_id}/whatif", s.whatifFlow)
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

// importRequest is the flow-as-code document — the same shape `…/export`
// produces. Version and Etag are accepted so an exported doc round-trips, but
// they are advisory: the import always publishes onto the live latest version.
type importRequest struct {
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Graph       events.Graph    `json:"graph"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Version     int             `json:"version,omitempty"`
	Etag        string          `json:"etag,omitempty"`
}

// importFlow upserts a flow from an exported document via the command layer,
// which folds the authoritative log: it reuses the flow with the given slug (or
// creates it) and publishes the graph as a new version. Re-importing identical
// content is a no-op (the live latest version already matches), so it is safe to
// run from CI / GitOps on every push. A 200 means no-op, a 201 means it wrote.
func (s *Service) importFlow(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req importRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.cmd.ImportFlow(r.Context(), id, domain.ImportFlow{
		Slug:        req.Slug,
		Name:        req.Name,
		Graph:       req.Graph,
		InputSchema: req.InputSchema,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	body := map[string]any{
		"flow_id": res.FlowID, "slug": req.Slug, "version": res.Version,
		"etag": res.Etag, "created": res.Created, "published": res.Published,
	}
	if !res.Published {
		httpx.JSON(w, http.StatusOK, body)
		return
	}
	body["event_id"] = res.Event.ID
	body["seq"] = res.Event.Seq
	httpx.JSON(w, http.StatusCreated, body)
}

type bundleRequest struct {
	Flows []importRequest `json:"flows"`
}

// bundleResult is one flow's outcome within a bundle import.
type bundleResult struct {
	Slug      string `json:"slug"`
	FlowID    string `json:"flow_id,omitempty"`
	Version   int    `json:"version,omitempty"`
	Created   bool   `json:"created"`
	Published bool   `json:"published"`
	Error     string `json:"error,omitempty"`
}

// importBundle imports many flows in one document (a GitOps repo of flows). It
// is best-effort: each flow is imported independently against the authoritative
// log, and a failing flow is reported in its result rather than aborting the
// rest — so the response is the per-flow truth, not all-or-nothing. The status
// is 200 (a batch report); per-flow success/failure is in each result.
func (s *Service) importBundle(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req bundleRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Flows) == 0 {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("bundle: flows is required"))
		return
	}
	results := make([]bundleResult, 0, len(req.Flows))
	var published, failed int
	for _, f := range req.Flows {
		res, err := s.cmd.ImportFlow(r.Context(), id, domain.ImportFlow{
			Slug:        f.Slug,
			Name:        f.Name,
			Graph:       f.Graph,
			InputSchema: f.InputSchema,
		})
		if err != nil {
			results = append(results, bundleResult{Slug: f.Slug, Error: err.Error()})
			failed++
			continue
		}
		results = append(results, bundleResult{
			Slug: f.Slug, FlowID: res.FlowID, Version: res.Version,
			Created: res.Created, Published: res.Published,
		})
		if res.Published {
			published++
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"results": results, "published": published, "failed": failed,
		"unchanged": len(req.Flows) - published - failed,
	})
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

type promoteRequest struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Force bool   `json:"force,omitempty"` // override the firing-monitor gate
}

type promotionStageRequest struct {
	RequireAssertions       *bool `json:"require_assertions,omitempty"`
	RequireNoFiringMonitors *bool `json:"require_no_firing_monitors,omitempty"`
	AllowForce              *bool `json:"allow_force,omitempty"`
	RequireReview           *bool `json:"require_review,omitempty"`
}

type promotionPolicyRequest struct {
	Policy map[string]promotionStageRequest `json:"policy"`
}

func (s *Service) getPromotionPolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	fv, found, err := flows.Read(r.Context(), s.store, id, r.PathValue("flow_id"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("flow not found"))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"policy": fv.PromotionPolicy})
}

func (s *Service) setPromotionPolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req promotionPolicyRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	policy := mergePromotionPolicy(req.Policy)
	e, err := s.cmd.SetPromotionPolicy(r.Context(), id, domain.SetPromotionPolicy{
		FlowID: r.PathValue("flow_id"),
		Policy: policy,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"policy": policy, "event_id": e.ID, "seq": e.Seq})
}

func mergePromotionPolicy(req map[string]promotionStageRequest) map[string]events.PromotionStagePolicy {
	policy := flows.DefaultPromotionPolicy()
	for env, patch := range req {
		stage := policy[env]
		if patch.RequireAssertions != nil {
			stage.RequireAssertions = *patch.RequireAssertions
		}
		if patch.RequireNoFiringMonitors != nil {
			stage.RequireNoFiringMonitors = *patch.RequireNoFiringMonitors
		}
		if patch.AllowForce != nil {
			stage.AllowForce = *patch.AllowForce
		}
		if patch.RequireReview != nil {
			stage.RequireReview = *patch.RequireReview
		}
		if env == domain.EnvProduction {
			stage.RequireReview = true
		}
		policy[env] = stage
	}
	return policy
}

type shadowRequest struct {
	Environment string `json:"environment"`
	Version     int    `json:"version"` // 0 clears the shadow
}

// getShadow returns the current shadow assignments and the divergence report.
func (s *Service) getShadow(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	flowID := r.PathValue("flow_id")
	fv, found, err := flows.Read(r.Context(), s.store, id, flowID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("flow not found"))
		return
	}
	report, _, err := shadow.Read(r.Context(), s.store, id, flowID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"shadows": fv.Shadows, "report": report.ByEnv})
}

// setShadow assigns (or clears, with version 0) the shadow version for an env.
func (s *Service) setShadow(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req shadowRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	e, err := s.cmd.SetShadow(r.Context(), id, domain.SetShadow{
		FlowID:      r.PathValue("flow_id"),
		Environment: req.Environment,
		Version:     req.Version,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"environment": req.Environment, "version": req.Version, "event_id": e.ID, "seq": e.Seq,
	})
}

// promote ships the version live in `from` to `to`, carrying the champion only
// (a promotion is a known-good version moving up the chain, not an A/B split). It
// honors the same gate as a direct action on the target: promoting into a
// non-production env deploys immediately; promoting into production opens a
// maker-checker deployment request instead of deploying directly.
func (s *Service) promote(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var req promoteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if !domain.ValidEnvironment(req.From) || !domain.ValidEnvironment(req.To) {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("promote: from and to must be valid environments"))
		return
	}
	if req.From == req.To {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("promote: from and to must differ"))
		return
	}
	flowID := r.PathValue("flow_id")
	fv, found, err := flows.Read(r.Context(), s.store, id, flowID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("flow not found"))
		return
	}
	src, ok := fv.Deployments[req.From]
	if !ok || src.Version == 0 {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("promote: nothing deployed in %q to promote", req.From))
		return
	}
	stage := fv.PromotionPolicy[req.To]
	if err := s.checkPromotionGates(r.Context(), id, flowID, src.Version, req.To, stage, req.Force); err != nil {
		httpx.Error(w, http.StatusConflict, err)
		return
	}
	cmd := domain.DeployVersion{FlowID: flowID, Environment: req.To, Version: src.Version}
	if stage.RequireReview {
		reqID, e, derr := s.cmd.RequestDeployment(r.Context(), id, cmd)
		if derr != nil {
			httpx.Error(w, http.StatusBadRequest, derr)
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{
			"promoted": false, "pending": true, "request_id": reqID, "from": req.From, "to": req.To,
			"version": src.Version, "event_id": e.ID, "seq": e.Seq,
		})
		return
	}
	e, derr := s.cmd.Deploy(r.Context(), id, cmd)
	if derr != nil {
		httpx.Error(w, http.StatusBadRequest, derr)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"promoted": true, "from": req.From, "to": req.To, "version": src.Version,
		"event_id": e.ID, "seq": e.Seq,
	})
}

func (s *Service) checkPromotionGates(ctx context.Context, id identity.Identity, flowID string, version int, target string, stage events.PromotionStagePolicy, force bool) error {
	if force && stage.AllowForce {
		return nil
	}
	advice := "fix them or pass force to override"
	if !stage.AllowForce {
		advice = fmt.Sprintf("fix them; force is disabled for %s", target)
	}
	if stage.RequireNoFiringMonitors {
		// Fail closed: if the monitor state can't be read, block rather than
		// promote a flow whose health is unknown.
		firing, err := s.firingMonitors(ctx, id, flowID)
		if err != nil {
			return fmt.Errorf("promote blocked: cannot evaluate monitors: %w", err)
		}
		if len(firing) > 0 {
			return fmt.Errorf("promote blocked: monitors firing (%s) — %s", strings.Join(firing, ", "), advice)
		}
	}
	if stage.RequireAssertions {
		rep, err := assertions.RunForFlow(ctx, s.store, id, flowID, version)
		if err != nil {
			return fmt.Errorf("promote blocked: cannot run assertions: %w", err)
		}
		if rep.Failed > 0 {
			return fmt.Errorf("promote blocked: %d/%d assertions failing on v%d — %s", rep.Failed, rep.Total, version, advice)
		}
	}
	return nil
}

// firingMonitors returns the metrics of the flow's monitors that are currently
// firing — the promotion gate's input.
func (s *Service) firingMonitors(ctx context.Context, id identity.Identity, flowID string) ([]string, error) {
	rules, err := monitor.ListByFlow(ctx, s.store, id, flowID)
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, nil
	}
	snap, err := monitor.LoadSnapshot(ctx, s.store, id, flowID)
	if err != nil {
		return nil, err
	}
	var firing []string
	for _, m := range rules {
		if st := monitor.Evaluate(snap, m.Rule()); st.Firing {
			firing = append(firing, m.Metric)
		}
	}
	return firing, nil
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
	var req struct {
		Reason string `json:"reason,omitempty"`
	}
	_ = httpx.DecodeJSON(r, &req)
	e, err := s.cmd.ApproveDeployment(r.Context(), id, r.PathValue("flow_id"), r.PathValue("req_id"), req.Reason)
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
	// EntityKey, when set, reads each row's entity id from that input field so a
	// multi-entity batch records (and seals PII) under the correct per-row subject
	// instead of misattributing every row to a single EntityID.
	EntityKey string `json:"entity_key,omitempty"`
}

type batchResult struct {
	Index       int            `json:"index"`
	EntityID    string         `json:"entity_id,omitempty"`
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
	resp := batchResponse{Total: len(req.Dataset), Results: make([]batchResult, 0, len(req.Dataset))}
	for i, input := range req.Dataset {
		// Per-row entity id when entity_key is set, else the batch-level EntityID.
		entityID := req.EntityID
		if req.EntityKey != "" {
			entityID = stringField(input, req.EntityKey)
			if entityID == "" {
				resp.Rejected++
				resp.Results = append(resp.Results, batchResult{Index: i, Status: "rejected", Error: "missing entity id field " + req.EntityKey})
				continue
			}
		}
		ref := command.EntityRef{Type: req.EntityType, ID: entityID}
		res, err := s.decide.Decide(r.Context(), id, slug, env, input, ref)
		if err != nil {
			resp.Rejected++
			resp.Results = append(resp.Results, batchResult{Index: i, EntityID: entityID, Status: "rejected", Error: err.Error()})
			continue
		}
		switch res.Status {
		case "completed":
			resp.Completed++
		case "failed":
			resp.Failed++
		}
		resp.Results = append(resp.Results, batchResult{
			Index: i, EntityID: entityID, DecisionID: res.DecisionID, Status: res.Status, Data: res.Output,
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
	page, err := history.ListPage(r.Context(), s.store, id, decisionFilter(r))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	// Fail closed: if the masking config can't be read, surface the error rather
	// than serving records that should have been masked.
	fields, ferr := privacy.Fields(r.Context(), s.store, id)
	if ferr != nil {
		httpx.Error(w, http.StatusInternalServerError, ferr)
		return
	}
	for i := range page.Records {
		page.Records[i] = maskRecord(page.Records[i], fields)
	}
	httpx.JSON(w, http.StatusOK, page)
}

// decisionFilter parses the Decisions list query string (flow/env/status/variant,
// a decision-id search q, an RFC3339 since/until range, and limit/offset).
func decisionFilter(r *http.Request) history.Filter {
	q := r.URL.Query()
	f := history.Filter{
		Slug:        q.Get("flow"),
		Environment: q.Get("env"),
		Status:      q.Get("status"),
		Variant:     q.Get("variant"),
		Query:       q.Get("q"),
		Limit:       atoiDefault(q.Get("limit"), 0),
		Offset:      atoiDefault(q.Get("offset"), 0),
	}
	if t, err := time.Parse(time.RFC3339, q.Get("since")); err == nil {
		f.Since = t
	}
	if t, err := time.Parse(time.RFC3339, q.Get("until")); err == nil {
		f.Until = t
	}
	return f
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func (s *Service) getDecision(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	rec, found, err := history.Read(r.Context(), s.store, id, r.PathValue("decision_id"))
	if found && err == nil {
		rec, err = s.maskRecord(r.Context(), id, rec)
	}
	httpx.WriteOne(w, rec, found, err, "decision not found")
}

// maskRecord masks the configured sensitive fields in a decision record's input,
// output, and per-node outputs at the read boundary (the raw event log is intact).
func (s *Service) maskRecord(ctx context.Context, id identity.Identity, rec history.Record) (history.Record, error) {
	// Unseal crypto-shredded PII first (or surface "[erased]" once the subject is
	// erased), then apply read-boundary masking.
	if s.eraser != nil && rec.EntityType != "" && rec.EntityID != "" {
		subject := rec.EntityType + "/" + rec.EntityID
		// An erased subject yields "[erased]" inside OpenFields (not an error), so a
		// non-nil error is a genuine vault/decrypt fault — fail loudly.
		d, err := s.eraser.OpenFields(ctx, id, subject, rec.Data)
		if err != nil {
			return history.Record{}, fmt.Errorf("decision-engine: unseal data: %w", err)
		}
		rec.Data = d
		o, err := s.eraser.OpenFields(ctx, id, subject, rec.Output)
		if err != nil {
			return history.Record{}, fmt.Errorf("decision-engine: unseal output: %w", err)
		}
		rec.Output = o
		// Node-trace outputs are sealed at write time too — unseal them here (or
		// surface "[erased]") so the trace stays readable while erasure still shreds.
		for i := range rec.Nodes {
			n, err := s.eraser.OpenFields(ctx, id, subject, rec.Nodes[i].Output)
			if err != nil {
				return history.Record{}, fmt.Errorf("decision-engine: unseal node %q: %w", rec.Nodes[i].NodeID, err)
			}
			rec.Nodes[i].Output = n
		}
	}
	// Fail closed: a masking-config read error must block the record, not serve it raw.
	fields, err := privacy.Fields(ctx, s.store, id)
	if err != nil {
		return history.Record{}, fmt.Errorf("decision-engine: read privacy config: %w", err)
	}
	return maskRecord(rec, fields), nil
}

func maskRecord(rec history.Record, fields map[string]bool) history.Record {
	if len(fields) == 0 {
		return rec
	}
	rec.Data = privacy.Mask(rec.Data, fields)
	rec.Output = privacy.Mask(rec.Output, fields)
	for i := range rec.Nodes {
		rec.Nodes[i].Output = privacy.Mask(rec.Nodes[i].Output, fields)
	}
	return rec
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
