// SPDX-License-Identifier: AGPL-3.0-or-later

package authoring_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/authoring"
	enginecommand "github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/privacy"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestCanonicalImportHTTPIsIdempotentAndNeverPublishes(t *testing.T) {
	ctx := context.Background()
	log, st := testutil.NewLogStore(t)
	id := identity.Identity{Org: "acme", Workspace: "risk", Actor: "ci"}
	engine := enginecommand.NewHandler(log)
	handler := authoring.NewHandler(log, st, engine)
	api := testutil.StartAPI(
		t, log, st, "ci-key", id, authoring.New(handler, st).Routes,
		flows.Projector{}, authoring.Projector{}, privacy.Projector{},
	)
	asset := authoring.CanonicalFlow{
		FormatVersion: authoring.CanonicalFormatV1, Kind: authoring.CanonicalKindFlow,
		Slug: "repository-credit", Name: "Repository credit",
		Graph: completeGraph("Repository rule"),
	}
	status, body := authoringRequest(t, api, asset, "")
	if status != http.StatusBadRequest {
		t.Fatalf("import without idempotency key = %d body=%s", status, body)
	}
	status, body = authoringRequest(t, api, asset, "commit-a")
	if status != http.StatusCreated {
		t.Fatalf("first import = %d body=%s", status, body)
	}
	var first authoring.ImportResult
	if err := json.Unmarshal(body, &first); err != nil {
		t.Fatalf("decode first import: %v", err)
	}
	status, body = authoringRequest(t, api, asset, "commit-a")
	if status != http.StatusCreated {
		t.Fatalf("retry import = %d body=%s", status, body)
	}
	var retry authoring.ImportResult
	if err := json.Unmarshal(body, &retry); err != nil {
		t.Fatalf("decode retry import: %v", err)
	}
	if first.FlowID != retry.FlowID || first.DraftID != retry.DraftID {
		t.Fatalf("retry = %+v, want original %+v", retry, first)
	}
	asset.Name = "Different content"
	status, body = authoringRequest(t, api, asset, "commit-a")
	if status != http.StatusConflict {
		t.Fatalf("changed retry = %d body=%s", status, body)
	}

	var flow flows.FlowView
	api.Request(
		t, http.MethodGet, "/v1/authoring/drafts?flow_id="+first.FlowID,
		nil, http.StatusOK, nil,
	)
	api.Request(
		t, http.MethodGet, "/v1/authoring/drafts/"+first.DraftID+"/export",
		nil, http.StatusOK, nil,
	)
	envelopes, err := log.ReadTenantStream(
		ctx, id.Org, id.Workspace, authoring.Stream, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	exported := 0
	for _, envelope := range envelopes {
		if envelope.Type == authoring.TypeDraftExported && envelope.Actor == id.Actor {
			exported++
		}
	}
	if exported != 1 {
		t.Fatal("canonical draft export did not append actor-attributed audit evidence")
	}
	if _, err := privacy.NewHandler(log).SetFields(ctx, id, []string{"ssn"}); err != nil {
		t.Fatalf("classify sensitive field: %v", err)
	}
	sensitiveGraph := completeGraph("Sensitive fixture")
	sensitiveGraph.Nodes[1].Config = json.RawMessage(
		`{"fixture":{"ssn":"123-45-6789"}}`,
	)
	api.Request(t, http.MethodPut, "/v1/authoring/drafts/"+first.DraftID, map[string]any{
		"expected_revision": 1,
		"title":             "Sensitive fixture must not leave the workspace",
		"graph":             sensitiveGraph,
	}, http.StatusOK, nil)
	api.Request(
		t, http.MethodGet, "/v1/authoring/drafts/"+first.DraftID+"/export",
		nil, http.StatusConflict, nil,
	)
	envelopes, err = log.ReadTenantStream(
		ctx, id.Org, id.Workspace, authoring.Stream, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	exported = 0
	for _, envelope := range envelopes {
		if envelope.Type == authoring.TypeDraftExported {
			exported++
		}
	}
	if exported != 1 {
		t.Fatalf("blocked export appended audit success evidence: %d export events", exported)
	}
	// Inspect the projection directly to prove import created identity + draft
	// but no immutable version.
	flow, found, err := flows.Read(ctx, st, id, first.FlowID)
	if err != nil || !found || flow.Latest != 0 || len(flow.Versions) != 0 {
		t.Fatalf("imported flow publication state = %+v found=%v err=%v", flow, found, err)
	}
}

func TestCanonicalBundlePreflightsEveryAssetBeforeWriting(t *testing.T) {
	ctx := context.Background()
	log, st := testutil.NewLogStore(t)
	id := identity.Identity{Org: "acme", Workspace: "risk", Actor: "ci"}
	handler := authoring.NewHandler(log, st, enginecommand.NewHandler(log))
	api := testutil.StartAPI(
		t, log, st, "ci-key", id, authoring.New(handler, st).Routes,
		flows.Projector{}, authoring.Projector{},
	)
	invalidGraph := completeGraph("Unknown reusable reference")
	invalidGraph.Nodes[1] = events.Node{
		ID: "rule", Type: events.NodeSubflow,
		Config: json.RawMessage(
			`{"component_slug":"missing-component","version":1}`,
		),
	}
	bundle := authoring.CanonicalBundle{
		FormatVersion: authoring.CanonicalFormatV1,
		Kind:          "bundle",
		Flows: []authoring.CanonicalFlow{
			{
				FormatVersion: authoring.CanonicalFormatV1,
				Kind:          authoring.CanonicalKindFlow,
				Slug:          "valid-first",
				Name:          "Valid first",
				Graph:         completeGraph("Valid"),
			},
			{
				FormatVersion: authoring.CanonicalFormatV1,
				Kind:          authoring.CanonicalKindFlow,
				Slug:          "invalid-second",
				Name:          "Invalid second",
				Graph:         invalidGraph,
			},
		},
	}
	api.RequestWithHeaders(
		t, http.MethodPost, "/v1/authoring/import-bundle", bundle,
		map[string]string{"Idempotency-Key": "bundle-preflight"},
		http.StatusBadRequest, nil,
	)
	for _, stream := range []string{authoring.Stream, events.StreamFlows} {
		envelopes, err := log.ReadTenantStream(ctx, id.Org, id.Workspace, stream, 0)
		if err != nil {
			t.Fatalf("read %s: %v", stream, err)
		}
		if len(envelopes) != 0 {
			t.Fatalf("invalid bundle partially wrote %s: %+v", stream, envelopes)
		}
	}
}

func authoringRequest(
	t *testing.T,
	api *testutil.API,
	body any,
	idempotencyKey string,
) (int, []byte) {
	t.Helper()
	document, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(
		http.MethodPost, api.Server.URL+"/v1/authoring/import",
		bytes.NewReader(document),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Api-Key", api.Key)
	request.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := api.Server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, responseBody
}

func TestCollaborativeAuthoringHTTPJourney(t *testing.T) {
	ctx := context.Background()
	log, st := testutil.NewLogStore(t)
	maker := identity.Identity{
		Org: "acme", Workspace: "risk", Actor: "maker",
	}
	checker := maker
	checker.Actor = "checker"
	engine := enginecommand.NewHandler(log)
	flowID, _, err := engine.CreateFlow(
		ctx, maker, domain.CreateFlow{Slug: "credit", Name: "Credit"},
	)
	if err != nil {
		t.Fatalf("create flow: %v", err)
	}
	if _, _, _, err := engine.PublishVersion(ctx, maker, domain.PublishVersion{
		FlowID: flowID, Graph: completeGraph("Initial rule"),
	}); err != nil {
		t.Fatalf("publish base: %v", err)
	}
	handler := authoring.NewHandler(log, st, engine)
	service := authoring.New(handler, st)
	api := testutil.StartAPI(
		t, log, st, "maker-key", maker, service.Routes,
		flows.Projector{}, authoring.Projector{},
	)
	checkerAPI := api.AddKey("checker-key", auth.APIKey{
		ID: "checker", Identity: checker, Scope: auth.ScopeAll, Role: auth.RoleApprover,
	})
	viewer := maker
	viewer.Actor = "viewer"
	viewerAPI := api.AddKey("viewer-key", auth.APIKey{
		ID: "viewer", Identity: viewer, Scope: auth.ScopeAll, Role: auth.RoleViewer,
	})
	operator := maker
	operator.Actor = "operator"
	operatorAPI := api.AddKey("operator-key", auth.APIKey{
		ID: "operator", Identity: operator, Scope: auth.ScopeAll, Role: auth.RoleOperator,
	})
	foreign := viewer
	foreign.Workspace = "other"
	foreignAPI := api.AddKey("foreign-key", auth.APIKey{
		ID: "foreign", Identity: foreign, Scope: auth.ScopeAll, Role: auth.RoleViewer,
	})

	var created struct {
		DraftID  string `json:"draft_id"`
		Revision int    `json:"revision"`
	}
	api.RequestWithHeaders(
		t, http.MethodPost, "/v1/authoring/drafts", map[string]any{
			"flow_id": flowID, "base_version": 1, "title": "Raise threshold",
			"graph": completeGraph("Proposed rule"),
		}, map[string]string{"Idempotency-Key": "draft-create"},
		http.StatusCreated, &created,
	)
	if created.DraftID == "" || created.Revision != 1 {
		t.Fatalf("created draft = %+v", created)
	}
	draftPath := "/v1/authoring/drafts/" + created.DraftID
	viewerAPI.Request(t, http.MethodGet, draftPath, nil, http.StatusOK, nil)
	if status := viewerAPI.RequestStatus(
		t, http.MethodPut, draftPath, map[string]any{}, nil,
	); status != http.StatusForbidden {
		t.Fatalf("viewer draft mutation status = %d, want 403", status)
	}
	if status := operatorAPI.RequestStatus(
		t, http.MethodPost, "/v1/authoring/changesets", map[string]any{}, nil,
	); status != http.StatusForbidden {
		t.Fatalf("operator changeset creation status = %d, want 403", status)
	}
	foreignAPI.Request(t, http.MethodGet, draftPath, nil, http.StatusNotFound, nil)

	save := map[string]any{
		"expected_revision": 1, "title": "Raise threshold",
		"graph": completeGraph("Reviewed proposal"),
	}
	var saved struct {
		Revision int `json:"revision"`
	}
	api.Request(
		t, http.MethodPut, "/v1/authoring/drafts/"+created.DraftID,
		save, http.StatusOK, &saved,
	)
	if saved.Revision != 2 {
		t.Fatalf("saved revision = %d", saved.Revision)
	}
	if status := checkerAPI.RequestStatus(
		t, http.MethodPut, "/v1/authoring/drafts/"+created.DraftID,
		save, nil,
	); status != http.StatusConflict {
		t.Fatalf("stale concurrent save status = %d, want 409", status)
	}

	var changeCreated struct {
		ChangeSetID string `json:"changeset_id"`
	}
	api.RequestWithHeaders(
		t, http.MethodPost, "/v1/authoring/changesets", map[string]any{
			"draft_id": created.DraftID, "draft_revision": 2,
			"title": "Raise threshold", "required_checks": []string{"flow-validation"},
			"reviewers": []string{"checker"},
		}, map[string]string{"Idempotency-Key": "changeset-create"},
		http.StatusCreated, &changeCreated,
	)
	if changeCreated.ChangeSetID == "" {
		t.Fatal("changeset id is empty")
	}
	// The draft continues to autosave after submission material is pinned.
	api.Request(t, http.MethodPut, "/v1/authoring/drafts/"+created.DraftID, map[string]any{
		"expected_revision": 2, "title": "Later iteration",
		"graph": completeGraph("Unreviewed later edit"),
	}, http.StatusOK, nil)
	changePath := "/v1/authoring/changesets/" + changeCreated.ChangeSetID
	api.Request(t, http.MethodPost, changePath+"/checks", map[string]any{
		"name": "flow-validation", "status": "failed",
		"evidence": "caller cannot spoof the built-in result",
	}, http.StatusOK, nil)
	api.Request(t, http.MethodPost, changePath+"/submit", map[string]any{}, http.StatusOK, nil)
	api.Request(t, http.MethodPost, changePath+"/review", map[string]any{
		"decision": "approve",
	}, http.StatusBadRequest, nil)
	checkerAPI.Request(t, http.MethodPost, changePath+"/review", map[string]any{
		"decision": "approve", "reason": "Independent review complete",
	}, http.StatusOK, nil)
	var publication struct {
		Version int `json:"version"`
	}
	checkerAPI.Request(
		t, http.MethodPost, changePath+"/publish", map[string]any{},
		http.StatusCreated, &publication,
	)
	if publication.Version != 2 {
		t.Fatalf("published version = %d", publication.Version)
	}
	var view authoring.ChangeSetView
	api.Request(t, http.MethodGet, changePath, nil, http.StatusOK, &view)
	if view.State != authoring.ChangeSetPublished ||
		view.Checks["flow-validation"].Status != authoring.CheckPassed {
		t.Fatalf("published changeset = %+v", view)
	}
	flow, found, err := flows.Read(ctx, st, maker, flowID)
	if err != nil || !found {
		t.Fatalf("read flow: found %v, err %v", found, err)
	}
	published := flow.Versions[1]
	if published.ChangeSetID != changeCreated.ChangeSetID ||
		published.SourceGraph == nil ||
		published.SourceGraph.Nodes[1].Name != "Reviewed proposal" {
		t.Fatalf("reviewed publication lineage = %+v", published)
	}

	api.Request(t, http.MethodPut, "/v1/authoring/drafts/"+created.DraftID+"/presence", map[string]any{
		"display_name": "Maker", "revision": 3, "selected_id": "rule",
	}, http.StatusOK, nil)
	var presence struct {
		Items []authoring.Presence `json:"presence"`
	}
	api.Request(
		t, http.MethodGet, "/v1/authoring/drafts/"+created.DraftID+"/presence",
		nil, http.StatusOK, &presence,
	)
	if len(presence.Items) != 1 || presence.Items[0].SelectedID != "rule" {
		t.Fatalf("presence = %+v", presence.Items)
	}
}

func completeGraph(ruleName string) events.Graph {
	return events.Graph{
		Nodes: []events.Node{
			{ID: "input", Type: events.NodeInput},
			{ID: "rule", Type: events.NodeRule, Name: ruleName},
			{ID: "output", Type: events.NodeOutput},
		},
		Edges: []events.Edge{
			{From: "input", To: "rule"},
			{From: "rule", To: "output"},
		},
	}
}
