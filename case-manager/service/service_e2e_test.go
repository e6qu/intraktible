// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/command"
	"github.com/e6qu/intraktible/case-manager/service"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
)

func start(t *testing.T) *testutil.API {
	t.Helper()
	log, st := testutil.NewLogStore(t)
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

	// SLA fields are computed at read time: a 5-day case opened just now has
	// roughly its full window left and is on track.
	var c cases.CaseView
	api.Request(t, http.MethodGet, "/v1/cases/"+opened.CaseID, nil, http.StatusOK, &c)
	if c.SLAState != "on_track" {
		t.Fatalf("sla_state = %q, want on_track", c.SLAState)
	}
	if c.DaysLeft < 4 || c.DaysLeft > 5 {
		t.Fatalf("days_left = %d, want 4-5 for a freshly opened 5-day case", c.DaysLeft)
	}

	// The queue summary rolls up the (filtered) set.
	var sum cases.Summary
	api.Request(t, http.MethodGet, "/v1/cases/summary", nil, http.StatusOK, &sum)
	if sum.Total != 1 || sum.ByStatus["in_progress"] != 1 {
		t.Fatalf("summary = %+v, want total 1 / in_progress 1", sum)
	}
}

func TestCaseSLASweepBreachesOverdue(t *testing.T) {
	// Run the sweep two days in the (simulated) future so a 1-day-SLA case is genuinely
	// overdue — sla_days:0 no longer means "immediately overdue" (it defaults to a
	// sensible window, matching the read model).
	log, st := testutil.NewLogStore(t)
	svc := service.New(command.NewHandler(log), st).WithNow(func() time.Time { return time.Now().UTC().Add(48 * time.Hour) })
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}
	api := testutil.StartAPI(t, log, st, "test-key", id, svc.Routes, cases.Projector{})

	var opened struct {
		CaseID string `json:"case_id"`
	}
	api.Request(t, http.MethodPost, "/v1/cases",
		map[string]any{"company_name": "Acme Corp", "case_type": "aml", "sla_days": 1},
		http.StatusCreated, &opened)

	// The sweep emits a breach event for it.
	var sweep struct {
		Count int `json:"count"`
	}
	api.Request(t, http.MethodPost, "/v1/cases/sla-sweep", nil, http.StatusOK, &sweep)
	if sweep.Count != 1 {
		t.Fatalf("sweep breached %d, want 1", sweep.Count)
	}

	// The projection now marks the case breached with an audit entry.
	if !testutil.Eventually(t, func() bool {
		var c cases.CaseView
		api.Request(t, http.MethodGet, "/v1/cases/"+opened.CaseID, nil, http.StatusOK, &c)
		breachAudited := false
		for _, a := range c.Audit {
			if a.Type == "sla_breached" {
				breachAudited = true
			}
		}
		return c.SLABreached && breachAudited
	}) {
		t.Fatal("case never reflected the SLA breach")
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
