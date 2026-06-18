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
		dec, err = c.Decide(ctx, "sdk-demo", "sandbox", client.DecideRequest{Data: map[string]any{}})
		return err == nil && dec.Status == "completed" && dec.Data["decision"] == "OK"
	}) {
		t.Fatalf("Decide never produced the expected output: %+v err=%v", dec, err)
	}

	// The decision is readable from history.
	got, err := c.GetDecision(ctx, dec.DecisionID)
	if err != nil || got.DecisionID != dec.DecisionID || got.Slug != "sdk-demo" {
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
}
