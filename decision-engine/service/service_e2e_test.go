// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"net/http"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/decision-engine/service"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func startEngine(t *testing.T) *testutil.API {
	t.Helper()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	st := store.NewMemory()
	svc := service.New(command.NewHandler(log), st)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "author"}
	return testutil.StartAPI(t, log, st, "test-key", id, svc.Routes, flows.Projector{})
}

func TestEngineAPIEndToEnd(t *testing.T) {
	api := startEngine(t)

	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "onboarding", "name": "Onboarding"}, http.StatusCreated, &created)
	if created.FlowID == "" {
		t.Fatal("create did not return a flow id")
	}

	var published struct {
		Version int    `json:"version"`
		Etag    string `json:"etag"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.LinearGraph()}, http.StatusCreated, &published)
	if published.Version != 1 || published.Etag == "" {
		t.Fatalf("publish returned %+v", published)
	}

	// Read model is async; poll the GET endpoint until the version appears.
	if !testutil.Eventually(t, func() bool {
		var fv flows.FlowView
		api.Request(t, http.MethodGet, "/v1/flows/"+created.FlowID, nil, http.StatusOK, &fv)
		return fv.Latest == 1 && len(fv.Versions) == 1 && fv.Slug == "onboarding"
	}) {
		t.Fatal("GET flow never reflected the published version")
	}

	var list struct {
		Flows []flows.FlowView `json:"flows"`
	}
	api.Request(t, http.MethodGet, "/v1/flows", nil, http.StatusOK, &list)
	if len(list.Flows) != 1 {
		t.Fatalf("list returned %d flows, want 1", len(list.Flows))
	}
}

func TestEngineAPIValidationErrors(t *testing.T) {
	api := startEngine(t)

	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "dup", "name": "A"}, http.StatusCreated, &created)

	// Duplicate slug -> 400.
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "dup", "name": "B"}, http.StatusBadRequest, nil)

	// Invalid (cyclic) graph -> 400.
	cyclic := flowtest.LinearGraph()
	cyclic.Edges = append(cyclic.Edges, events.Edge{From: "out", To: "r"})
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": cyclic}, http.StatusBadRequest, nil)

	// Publishing to an unknown flow -> 400.
	api.Request(t, http.MethodPost, "/v1/flows/ghost/versions",
		map[string]any{"graph": flowtest.LinearGraph()}, http.StatusBadRequest, nil)

	// Unknown flow GET -> 404.
	api.Request(t, http.MethodGet, "/v1/flows/ghost", nil, http.StatusNotFound, nil)
}

func TestEngineAPIRequiresAuth(t *testing.T) {
	api := startEngine(t)
	resp, err := http.Get(api.Server.URL + "/v1/flows")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request -> %d, want 401", resp.StatusCode)
	}
}
