// SPDX-License-Identifier: AGPL-3.0-or-later

package client_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/client"
	"github.com/e6qu/intraktible/modeling/domain"
)

type modelingRoundTrip func(*http.Request) (*http.Response, error)

func (roundTrip modelingRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestGoSDKModelingAndGovernanceSurface(t *testing.T) {
	var requests []string
	transport := modelingRoundTrip(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("X-Api-Key"); got != "model-key" {
			t.Fatalf("X-Api-Key = %q", got)
		}
		path := request.URL.EscapedPath()
		requests = append(requests, request.Method+" "+path+"?"+request.URL.RawQuery)
		body := `{}`
		switch path {
		case "/v1/modeling/schemas":
			if request.Method == http.MethodGet {
				body = `{"schemas":[]}`
			} else {
				body = `{"event_id":"schema","seq":1}`
			}
		case "/v1/modeling/schemas/event/applicant/versions/2/approval-request":
			body = `{"request_id":"schema-request"}`
		case "/v1/modeling/schema-approval/schema-request/decision",
			"/v1/modeling/schemas/event/applicant/versions/2/retire",
			"/v1/modeling/quality/incidents/incident%2F1/acknowledge",
			"/v1/modeling/quality/incidents/incident%2F1/resolve",
			"/v1/modeling/jobs/job%2F1/pause",
			"/v1/modeling/jobs/job%2F1/resume",
			"/v1/modeling/jobs/job%2F1/retry",
			"/v1/modeling/jobs/job%2F1/cancel":
			body = `{"event_id":"command","seq":2}`
		case "/v1/modeling/quality/observations":
			body = `{"observations":[]}`
		case "/v1/modeling/quality/incidents":
			body = `{"incidents":[]}`
		case "/v1/modeling/source-health":
			body = `{"sources":[]}`
		case "/v1/modeling/features":
			body = `{"features":[]}`
		case "/v1/modeling/datasets":
			if request.Method == http.MethodGet {
				body = `{"datasets":[]}`
			} else {
				body = `{"event_id":"dataset","seq":3}`
			}
		case "/v1/modeling/snapshots":
			body = `{"snapshots":[]}`
		case "/v1/modeling/snapshots/snapshot%2F1/export":
			body = `[]`
		case "/v1/modeling/jobs":
			body = `{"jobs":[]}`
		case "/v1/modeling/training-jobs",
			"/v1/modeling/evaluation-jobs",
			"/v1/modeling/backfills",
			"/v1/modeling/datasets/risk%2Fdevelopment/versions/1/snapshots":
			body = `{"event_id":"queued","seq":4,"job_id":"job-1"}`
		case "/v1/modeling/artifacts":
			if request.Method == http.MethodGet {
				body = `{"artifacts":[]}`
			} else {
				body = `{"event_id":"artifact","seq":5}`
			}
		case "/v1/modeling/artifacts/artifact%2F1/stage":
			body = `{"event_id":"artifact-stage","seq":6}`
		case "/v1/modeling/artifacts/artifact%2F1/verify":
			body = `{"valid":true}`
		case "/v1/modeling/evaluations":
			body = `{"evaluations":[]}`
		case "/v1/modeling/materializations":
			body = `{"materializations":[]}`
		case "/v1/models":
			body = `{"models":[]}`
		case "/v1/models/model%2F1/approval-request":
			body = `{"request_id":"model-request"}`
		case "/v1/models/model%2F1/validation",
			"/v1/models/model%2F1/approve",
			"/v1/models/model%2F1/retire":
			body = `{"event_id":"model-command","seq":5}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})
	sdk := client.New(
		"https://intraktible.example", "model-key",
		client.WithHTTPClient(&http.Client{Transport: transport}),
	)
	ctx := context.Background()
	ref := domain.SchemaRef{
		Kind: domain.SchemaKindEvent, EntityType: "applicant", EventName: "risk/signal",
	}

	mustNoError(t, callError(sdk.DefineSchema(ctx, domain.SchemaSpec{})))
	mustNoError(t, callError(sdk.ListSchemas(ctx)))
	mustNoError(t, callError(sdk.GetSchema(ctx, ref)))
	requestID, err := sdk.RequestSchemaApproval(ctx, ref, 2)
	if err != nil || requestID != "schema-request" {
		t.Fatalf("schema approval request = %q, %v", requestID, err)
	}
	mustNoError(t, callError(sdk.DecideSchemaApproval(ctx, requestID, ref, true, "reviewed")))
	mustNoError(t, callError(sdk.RetireSchema(ctx, ref, 2, "superseded")))
	mustNoError(t, callError(sdk.ListQualityObservations(ctx)))
	mustNoError(t, callError(sdk.ListQualityIncidents(ctx)))
	mustNoError(t, callError(sdk.AcknowledgeQualityIncident(ctx, "incident/1", "triaged")))
	mustNoError(t, callError(sdk.ResolveQualityIncident(ctx, "incident/1", "repaired")))
	mustNoError(t, callError(sdk.ListSourceHealth(ctx)))
	mustNoError(t, callError(sdk.ListGovernedFeatures(ctx)))
	mustNoError(t, callError(sdk.DefineDataset(ctx, domain.DatasetSpec{})))
	mustNoError(t, callError(sdk.ListDatasets(ctx)))
	mustNoError(t, callError(sdk.RequestSnapshot(
		ctx, "risk/development", 1, domain.SnapshotRequest{},
	)))
	mustNoError(t, callError(sdk.ListSnapshots(ctx)))
	mustNoError(t, callError(sdk.ExportSnapshot(ctx, "snapshot/1", "json")))
	mustNoError(t, callError(sdk.ListModelingJobs(ctx)))
	mustNoError(t, callError(sdk.PauseModelingJob(ctx, "job/1", "maintenance")))
	mustNoError(t, callError(sdk.ResumeModelingJob(ctx, "job/1", "maintenance complete")))
	mustNoError(t, callError(sdk.RetryModelingJob(ctx, "job/1", "operator retry")))
	mustNoError(t, callError(sdk.CancelModelingJob(ctx, "job/1", "operator request")))
	mustNoError(t, callError(sdk.RequestTraining(ctx, domain.TrainingRequest{})))
	mustNoError(t, callError(sdk.RequestEvaluation(ctx, domain.EvaluationRequest{})))
	mustNoError(t, callError(sdk.ListArtifacts(ctx)))
	mustNoError(t, callError(sdk.RegisterExternalArtifact(
		ctx, domain.ArtifactRegistration{},
	)))
	mustNoError(t, callError(sdk.ChangeArtifactStage(
		ctx, "artifact/1", domain.ArtifactValidated, "evidence reviewed",
	)))
	if err := sdk.VerifyArtifact(ctx, "artifact/1"); err != nil {
		t.Fatal(err)
	}
	mustNoError(t, callError(sdk.ListEvaluations(ctx)))
	mustNoError(t, callError(sdk.RequestBackfill(ctx, domain.BackfillRequest{})))
	mustNoError(t, callError(sdk.ListMaterializations(ctx)))
	mustNoError(t, callError(sdk.ModelLineage(ctx, "model/1")))
	mustNoError(t, callError(sdk.CompareModels(ctx, "champion", "challenger")))
	mustNoError(t, callError(sdk.ListModels(ctx)))
	mustNoError(t, callError(sdk.GetModel(ctx, "model/1")))
	mustNoError(t, callError(sdk.RecordModelValidation(
		ctx, "model/1", client.ModelValidationRequest{
			Dataset: "risk/development", Metrics: map[string]float64{"auc": 0.82},
			Notes: "independent review", Passed: true, ArtifactID: "artifact/1",
			SnapshotID: "snapshot/1", EvaluationHash: strings.Repeat("a", 64),
			LeakagePassed: true, CalibrationReviewed: true,
			FairnessReviewed: true, ThresholdReviewed: true,
		},
	)))
	requestID, err = sdk.RequestModelApproval(ctx, "model/1")
	if err != nil || requestID != "model-request" {
		t.Fatalf("model approval request = %q, %v", requestID, err)
	}
	mustNoError(t, callError(sdk.DecideModelApproval(
		ctx, "model/1", requestID, true, "evidence complete",
	)))
	mustNoError(t, callError(sdk.RetireModel(ctx, "model/1", "superseded")))

	joined := strings.Join(requests, "\n")
	for _, want := range []string{
		"GET /v1/modeling/schemas/event/applicant?event_name=risk%2Fsignal",
		"POST /v1/modeling/schemas/event/applicant/versions/2/approval-request?event_name=risk%2Fsignal",
		"POST /v1/modeling/datasets/risk%2Fdevelopment/versions/1/snapshots?",
		"GET /v1/modeling/features?",
		"GET /v1/modeling/lineage/models/model%2F1?",
		"GET /v1/modeling/comparisons?challenger=challenger&champion=champion",
		"POST /v1/models/model%2F1/validation?",
		"POST /v1/models/model%2F1/retire?",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("requests do not contain %q:\n%s", want, joined)
		}
	}
}

func callError[T any](value T, err error) error {
	return err
}

func mustNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
