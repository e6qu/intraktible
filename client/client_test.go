// SPDX-License-Identifier: AGPL-3.0-or-later

package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e6qu/intraktible/client"
	enginecmd "github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/preapproval"
	engineservice "github.com/e6qu/intraktible/decision-engine/service"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestClientSendsAuthAndParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me" || r.Header.Get("X-Api-Key") != "secret" {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"org":"o","workspace":"w","actor":"ada","scope":"sandbox","role":"admin"}`))
	}))
	defer srv.Close()

	c := client.New(srv.URL, "secret", client.WithHTTPClient(srv.Client()))
	me, err := c.Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if me.Actor != "ada" || me.Role != "admin" {
		t.Fatalf("me = %+v", me)
	}
}

func TestClientMapsErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"flow not found"}`))
	}))
	defer srv.Close()

	c := client.New(srv.URL, "k", client.WithHTTPClient(srv.Client()))
	_, err := c.GetFlow(context.Background(), "missing")
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T (%v)", err, err)
	}
	if apiErr.Status != http.StatusNotFound || apiErr.Message != "flow not found" {
		t.Fatalf("apiErr = %+v", apiErr)
	}
}

func TestEnterpriseE2ClientRoutes(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "e2-key" {
			http.Error(w, `{"error":"missing auth"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/experiments":
			_, _ = w.Write([]byte(`{"experiments":[]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/experiments/exp-1":
			_, _ = w.Write([]byte(`{"experiment_id":"exp-1"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/v1/experiments/exp-1":
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost &&
			r.URL.Path == "/v1/experiments/exp-1/launch-requests/launch-1/approve":
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/experiments/exp-1/promote":
			_, _ = w.Write([]byte(`{"pending":true,"request_id":"deploy-1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/population-jobs":
			_, _ = w.Write([]byte(`{"jobs":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/population-jobs/job-1/pause":
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/population-jobs/job-1/retry":
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, `{"error":"unexpected route"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := client.New(srv.URL, "e2-key", client.WithHTTPClient(srv.Client()))
	ctx := context.Background()
	experiments, err := c.ListExperiments(ctx)
	if err != nil || len(experiments) != 0 {
		t.Fatalf("ListExperiments() = %v, %v", experiments, err)
	}
	experiment, err := c.GetExperiment(ctx, "exp-1")
	if err != nil || experiment.ExperimentID != "exp-1" {
		t.Fatalf("GetExperiment() = %+v, %v", experiment, err)
	}
	if err := c.UpdateExperiment(ctx, "exp-1", client.ExperimentSpec{}); err != nil {
		t.Fatalf("UpdateExperiment(): %v", err)
	}
	if err := c.DecideExperimentLaunch(
		ctx, "exp-1", "launch-1", client.LaunchApprove, "independently reviewed",
	); err != nil {
		t.Fatalf("DecideExperimentLaunch(): %v", err)
	}
	promotion, err := c.PromoteExperimentWinner(ctx, "exp-1")
	if err != nil || !promotion.Pending || promotion.RequestID != "deploy-1" {
		t.Fatalf("PromoteExperimentWinner() = %+v, %v", promotion, err)
	}
	jobs, err := c.ListPopulationJobs(ctx)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("ListPopulationJobs() = %v, %v", jobs, err)
	}
	if err := c.TransitionPopulationJob(
		ctx, "job-1", client.PopulationPause, "operator pause",
	); err != nil {
		t.Fatalf("TransitionPopulationJob(): %v", err)
	}
	if err := c.RetryPopulationJob(ctx, "job-1", []int{2}); err != nil {
		t.Fatalf("RetryPopulationJob(): %v", err)
	}
}

func TestEnterpriseE3ClientRoutes(t *testing.T) {
	t.Parallel()

	responses := map[string]string{
		"GET /v1/cases":                                      `{"cases":[]}`,
		"GET /v1/cases/case-1":                               `{"case_id":"case-1"}`,
		"POST /v1/cases":                                     `{"case_id":"case-1"}`,
		"GET /v1/case-types":                                 `{"case_types":[]}`,
		"POST /v1/case-types":                                `{"version":2}`,
		"GET /v1/case-types/edd/versions/2":                  `{"key":"edd","version":2}`,
		"PUT /v1/case-queues/priority":                       `{}`,
		"GET /v1/case-queues":                                `{"queues":[]}`,
		"PUT /v1/case-reviewers/alice":                       `{}`,
		"GET /v1/case-reviewers":                             `{"reviewers":[]}`,
		"POST /v1/cases/bulk":                                `{"batch_id":"batch-1","operation":"assign","status":"completed","items":[]}`,
		"GET /v1/case-views":                                 `{"views":[]}`,
		"POST /v1/case-views":                                `{"view_id":"mine"}`,
		"DELETE /v1/case-views/mine":                         `{}`,
		"POST /v1/cases/case-1/assign":                       `{}`,
		"POST /v1/cases/case-1/status":                       `{}`,
		"POST /v1/cases/case-1/priority":                     `{}`,
		"PATCH /v1/cases/case-1/fields":                      `{}`,
		"POST /v1/cases/case-1/disposition":                  `{}`,
		"POST /v1/cases/case-1/evidence":                     `{}`,
		"POST /v1/cases/case-1/attachments":                  `{}`,
		"POST /v1/cases/case-1/attachments/file-1/access":    `{"storage_ref":"s3://approved/file-1"}`,
		"POST /v1/cases/case-1/route":                        `{"decision":{"queue":"priority"}}`,
		"POST /v1/cases/rebalance":                           `{"moved":{"case-1":"alice"}}`,
		"POST /v1/cases/case-1/qa/select":                    `{"selected":true}`,
		"POST /v1/cases/case-1/qa/review":                    `{}`,
		"POST /v1/cases/case-1/webhook/retry":                `{}`,
		"GET /v1/cases/duplicates":                           `{"duplicate_groups":[]}`,
		"GET /v1/cases/analytics":                            `{"open":1}`,
		"GET /v1/case-validated-outcomes":                    `{"validated_outcomes":[]}`,
		"GET /v1/cases/export?status=in_progress&format=csv": "case_id,status\ncase-1,in_progress\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "e3-key" {
			http.Error(w, `{"error":"missing auth"}`, http.StatusUnauthorized)
			return
		}
		key := r.Method + " " + r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		response, found := responses[key]
		if !found {
			http.Error(w, `{"error":"unexpected route"}`, http.StatusNotFound)
			return
		}
		if r.URL.Path == "/v1/cases/bulk" && r.Header.Get("Idempotency-Key") != "bulk-key" {
			http.Error(w, `{"error":"missing idempotency"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	defer srv.Close()

	c := client.New(srv.URL, "e3-key", client.WithHTTPClient(srv.Client()))
	ctx := context.Background()
	filter := client.CaseFilter{Status: "in_progress"}

	if got, err := c.ListCases(ctx, client.CaseFilter{}); err != nil || len(got) != 0 {
		t.Fatalf("ListCases() = %+v, %v", got, err)
	}
	if got, err := c.GetCase(ctx, "case-1"); err != nil || got.CaseID != "case-1" {
		t.Fatalf("GetCase() = %+v, %v", got, err)
	}
	if got, err := c.CreateCase(ctx, client.CaseCreate{CaseType: "edd"}); err != nil || got != "case-1" {
		t.Fatalf("CreateCase() = %q, %v", got, err)
	}
	if got, err := c.ListCaseTypes(ctx); err != nil || len(got) != 0 {
		t.Fatalf("ListCaseTypes() = %+v, %v", got, err)
	}
	if got, err := c.PublishCaseType(ctx, client.CaseTypeDefinition{Key: "edd"}); err != nil || got != 2 {
		t.Fatalf("PublishCaseType() = %d, %v", got, err)
	}
	if got, err := c.GetCaseType(ctx, "edd", 2); err != nil || got.Version != 2 {
		t.Fatalf("GetCaseType() = %+v, %v", got, err)
	}
	if err := c.ConfigureCaseQueue(ctx, client.CaseQueue{Key: "priority"}); err != nil {
		t.Fatalf("ConfigureCaseQueue(): %v", err)
	}
	if got, err := c.ListCaseQueues(ctx); err != nil || len(got) != 0 {
		t.Fatalf("ListCaseQueues() = %+v, %v", got, err)
	}
	if err := c.ConfigureCaseReviewer(ctx, client.CaseReviewer{Actor: "alice"}); err != nil {
		t.Fatalf("ConfigureCaseReviewer(): %v", err)
	}
	if got, err := c.ListCaseReviewers(ctx); err != nil || len(got) != 0 {
		t.Fatalf("ListCaseReviewers() = %+v, %v", got, err)
	}
	if got, err := c.BulkCases(ctx, client.CaseBulkRequest{Operation: "assign"}, "bulk-key"); err != nil ||
		got.BatchID != "batch-1" {
		t.Fatalf("BulkCases() = %+v, %v", got, err)
	}
	if got, err := c.ListCaseSavedViews(ctx); err != nil || len(got) != 0 {
		t.Fatalf("ListCaseSavedViews() = %+v, %v", got, err)
	}
	if got, err := c.SaveCaseView(ctx, "mine", "Mine", filter); err != nil || got != "mine" {
		t.Fatalf("SaveCaseView() = %q, %v", got, err)
	}
	if err := c.DeleteCaseView(ctx, "mine"); err != nil {
		t.Fatalf("DeleteCaseView(): %v", err)
	}
	for name, run := range map[string]func() error{
		"AssignCase":       func() error { return c.AssignCase(ctx, "case-1", "alice", false) },
		"SetCaseStatus":    func() error { return c.SetCaseStatus(ctx, "case-1", "in_progress") },
		"SetCasePriority":  func() error { return c.SetCasePriority(ctx, "case-1", "high") },
		"UpdateCaseFields": func() error { return c.UpdateCaseFields(ctx, "case-1", map[string]any{"risk": 7}) },
		"DispositionCase":  func() error { return c.DispositionCase(ctx, "case-1", "clear", "verified", "done", false) },
		"LinkCaseEvidence": func() error { return c.LinkCaseEvidence(ctx, "case-1", map[string]any{"evidence_id": "e-1"}) },
		"RegisterCaseAttachment": func() error {
			return c.RegisterCaseAttachment(ctx, "case-1", map[string]any{"attachment_id": "file-1"})
		},
		"ReviewCaseQA":     func() error { return c.ReviewCaseQA(ctx, "case-1", "sample-1", "clear", "verified", "agree", false) },
		"RetryCaseWebhook": func() error { return c.RetryCaseWebhook(ctx, "case-1", "operator retry") },
	} {
		if err := run(); err != nil {
			t.Fatalf("%s(): %v", name, err)
		}
	}
	if got, err := c.AccessCaseAttachment(ctx, "case-1", "file-1", "review"); err != nil ||
		got != "s3://approved/file-1" {
		t.Fatalf("AccessCaseAttachment() = %q, %v", got, err)
	}
	if got, err := c.RouteCase(ctx, "case-1"); err != nil || len(got) == 0 {
		t.Fatalf("RouteCase() = %s, %v", got, err)
	}
	if got, err := c.RebalanceCases(ctx); err != nil || len(got) == 0 {
		t.Fatalf("RebalanceCases() = %s, %v", got, err)
	}
	if got, err := c.SelectCaseQA(ctx, "case-1", "sample-1", "bob", 10000); err != nil || !got {
		t.Fatalf("SelectCaseQA() = %t, %v", got, err)
	}
	if got, err := c.CaseDuplicates(ctx); err != nil || len(got) != 0 {
		t.Fatalf("CaseDuplicates() = %+v, %v", got, err)
	}
	if got, err := c.CaseAnalytics(ctx); err != nil || len(got) == 0 {
		t.Fatalf("CaseAnalytics() = %s, %v", got, err)
	}
	if got, err := c.ValidatedCaseOutcomes(ctx); err != nil || len(got) != 0 {
		t.Fatalf("ValidatedCaseOutcomes() = %+v, %v", got, err)
	}
	if got, err := c.ExportCaseAudit(ctx, "csv", filter); err != nil ||
		string(got) != "case_id,status\ncase-1,in_progress\n" {
		t.Fatalf("ExportCaseAudit() = %q, %v", got, err)
	}
}

// TestClientAgainstEngine drives the SDK against a real decision-engine service,
// proving the typed client matches the live contract end to end.
func TestClientAgainstEngine(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	svc := engineservice.New(enginecmd.NewHandler(log), enginecmd.NewDecideHandler(log, st), preapproval.NewHandler(log), st)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "sdk"}
	routes := func(mux *http.ServeMux) {
		svc.Routes(mux)
		mux.HandleFunc("GET /v1/me", httpx.MeHandler())
	}
	api := testutil.StartAPI(t, log, st, "sdk-key", id, routes, flows.Projector{}, history.Projector{})

	c := client.New(api.Server.URL, api.Key)
	ctx := context.Background()

	// Me round-trips the authenticated identity.
	me, err := c.Me(ctx)
	if err != nil || me.Actor != "sdk" || me.Org != "demo" {
		t.Fatalf("Me() = %+v, err=%v", me, err)
	}

	// Import a tiny flow as code (creates the flow and publishes v1).
	graph := json.RawMessage(`{"nodes":[` +
		`{"id":"in","type":"input"},` +
		`{"id":"a","type":"assignment","config":{"assignments":[{"target":"decision","expr":"'OK'"}]}},` +
		`{"id":"out","type":"output","config":{"fields":["decision"]}}` +
		`],"edges":[{"from":"in","to":"a"},{"from":"a","to":"out"}]}`)
	imp, err := c.ImportFlow(ctx, client.FlowDoc{Slug: "sdk-demo", Name: "SDK Demo", Graph: graph})
	if err != nil || !imp.Created || imp.Version != 1 {
		t.Fatalf("ImportFlow = %+v, err=%v", imp, err)
	}

	// CreateFlow makes a separate empty flow.
	otherID, err := c.CreateFlow(ctx, "sdk-other", "Other")
	if err != nil || otherID == "" {
		t.Fatalf("CreateFlow err=%v id=%q", err, otherID)
	}

	// Decide runs the imported flow (latest version, no deploy needed) — retry
	// while the flow projection catches up.
	var dec client.DecideResult
	if !testutil.Eventually(t, func() bool {
		dec, err = c.Decide(ctx, "sdk-demo", "sandbox", client.DecideRequest{
			Data: map[string]any{}, IdempotencyKey: "sdk-decision-1",
			BusinessReference: "application-1", CorrelationID: "trace-1",
			Metadata: map[string]any{"channel": "sdk"},
			Control:  &client.ExecutionControl{TimeoutMS: 1_000},
		})
		return err == nil && dec.Status == "completed" && dec.Data["decision"] == "OK"
	}) {
		t.Fatalf("Decide never produced the expected output: %+v err=%v", dec, err)
	}

	// The decision is readable from history.
	got, err := c.GetDecision(ctx, dec.DecisionID)
	if err != nil || got.DecisionID != dec.DecisionID || got.Slug != "sdk-demo" ||
		got.BusinessReference != "application-1" || got.CorrelationID != "trace-1" ||
		got.Metadata["channel"] != "sdk" {
		t.Fatalf("GetDecision = %+v, err=%v", got, err)
	}
	decisions, err := c.ListDecisions(ctx)
	if err != nil || len(decisions) == 0 {
		t.Fatalf("ListDecisions len=%d err=%v", len(decisions), err)
	}

	// Batch decide scores multiple rows.
	batch, err := c.DecideBatch(ctx, "sdk-demo", "sandbox", []map[string]any{{}, {}})
	if err != nil || len(batch.Results) != 2 {
		t.Fatalf("DecideBatch results=%d err=%v", len(batch.Results), err)
	}

	// Flow reads.
	list, err := c.ListFlows(ctx)
	if err != nil || len(list) < 2 {
		t.Fatalf("ListFlows len=%d err=%v", len(list), err)
	}
	flow, err := c.GetFlow(ctx, imp.FlowID)
	if err != nil || flow.Slug != "sdk-demo" || flow.Latest != 1 {
		t.Fatalf("GetFlow = %+v, err=%v", flow, err)
	}

	// An unknown flow is a typed transport error.
	_, decErr := c.Decide(ctx, "nope", "sandbox", client.DecideRequest{Data: map[string]any{}})
	var apiErr *client.APIError
	if !errors.As(decErr, &apiErr) {
		t.Fatalf("decide on an unknown flow: want *APIError, got %T (%v)", decErr, decErr)
	}

	// Deploy v1 to sandbox, then promote it up to staging (a non-prod target
	// deploys directly, so it is not pending). Promote reads the source
	// deployment from the projection, so retry while that catches up.
	if err := c.Deploy(ctx, imp.FlowID, "sandbox", 1); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	var prom client.PromoteResult
	if !testutil.Eventually(t, func() bool {
		prom, err = c.Promote(ctx, imp.FlowID, "sandbox", "staging", false)
		return err == nil && prom.Promoted
	}) {
		t.Fatalf("Promote never succeeded: %+v err=%v", prom, err)
	}
	if prom.Pending || prom.Version != 1 {
		t.Fatalf("Promote = %+v", prom)
	}

	// Import a bundle of two flows in one call.
	bundle, err := c.ImportBundle(ctx, []client.FlowDoc{
		{Slug: "sdk-bundle-a", Graph: graph},
		{Slug: "sdk-bundle-b", Graph: graph},
	})
	if err != nil || bundle.Published != 2 || bundle.Failed != 0 {
		t.Fatalf("ImportBundle = %+v, err=%v", bundle, err)
	}
}
