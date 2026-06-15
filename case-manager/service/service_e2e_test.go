// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"net/http"
	"testing"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/command"
	"github.com/e6qu/intraktible/case-manager/service"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func start(t *testing.T) *testutil.API {
	t.Helper()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	st := store.NewMemory()
	svc := service.New(command.NewHandler(log), st)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}
	return testutil.StartAPI(t, log, st, "test-key", id, svc.Routes, cases.Projector{})
}

func TestCaseAPIEndToEnd(t *testing.T) {
	api := start(t)

	var opened struct {
		CaseID string `json:"case_id"`
	}
	api.Request(t, http.MethodPost, "/v1/cases",
		map[string]any{"company_name": "Acme Corp", "case_type": "aml", "sla_days": 5},
		http.StatusCreated, &opened)
	if opened.CaseID == "" {
		t.Fatal("no case id returned")
	}

	api.Request(t, http.MethodPost, "/v1/cases/"+opened.CaseID+"/assign",
		map[string]string{"assignee": "adam"}, http.StatusAccepted, nil)
	api.Request(t, http.MethodPost, "/v1/cases/"+opened.CaseID+"/status",
		map[string]string{"status": "in_progress"}, http.StatusAccepted, nil)
	api.Request(t, http.MethodPost, "/v1/cases/"+opened.CaseID+"/notes",
		map[string]string{"text": "reviewed documents"}, http.StatusAccepted, nil)

	// The case detail reflects the lifecycle (async projection).
	if !testutil.Eventually(t, func() bool {
		var c cases.CaseView
		api.Request(t, http.MethodGet, "/v1/cases/"+opened.CaseID, nil, http.StatusOK, &c)
		return c.Status == "in_progress" && c.Assignee == "adam" && len(c.Notes) == 1 && len(c.Audit) == 4
	}) {
		t.Fatal("case detail never reflected the lifecycle")
	}

	// Queue filters.
	var list struct {
		Cases []cases.CaseView `json:"cases"`
	}
	api.Request(t, http.MethodGet, "/v1/cases?status=in_progress", nil, http.StatusOK, &list)
	if len(list.Cases) != 1 {
		t.Fatalf("status filter: %d cases, want 1", len(list.Cases))
	}
	api.Request(t, http.MethodGet, "/v1/cases?status=completed", nil, http.StatusOK, &list)
	if len(list.Cases) != 0 {
		t.Fatalf("completed filter: %d cases, want 0", len(list.Cases))
	}
}

func TestCaseAPIValidationAndAuth(t *testing.T) {
	api := start(t)

	// Missing company -> 400.
	api.Request(t, http.MethodPost, "/v1/cases", map[string]any{"case_type": "aml"}, http.StatusBadRequest, nil)
	// Unknown case -> 400.
	api.Request(t, http.MethodPost, "/v1/cases/ghost/assign", map[string]string{"assignee": "x"}, http.StatusBadRequest, nil)
	// Unauthenticated -> 401.
	resp, err := http.Get(api.Server.URL + "/v1/cases")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated -> %d, want 401", resp.StatusCode)
	}
}
