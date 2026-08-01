// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"testing"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
	"github.com/e6qu/intraktible/providers/command"
	"github.com/e6qu/intraktible/providers/projection"
	"github.com/e6qu/intraktible/providers/service"
)

func startProviders(t *testing.T) *testutil.API {
	log, st := testutil.NewLogStore(t)
	svc := service.New(command.NewHandler(log), st)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "installer"}
	return testutil.StartAPI(t, log, st, "test-key", id, svc.Routes, projection.Projector{})
}

func TestProviderLifecycleOverHTTP(t *testing.T) {
	api := startProviders(t)

	// Install v1.
	var installed struct {
		Version int `json:"version"`
	}
	api.Request(t, "POST", "/v1/providers", map[string]any{
		"name":        "bureau",
		"connector":   "credit-bureau",
		"description": "Credit bureau provider",
		"conformance": map[string]any{
			"schema":             `{"type":"object","properties":{"score":{"type":"number"}}}`,
			"timeout_seconds":    10,
			"max_retries":        2,
			"cost_per_fetch_usd": 0.05,
		},
	}, 202, &installed)
	if installed.Version != 1 {
		t.Fatalf("installed version = %d, want 1", installed.Version)
	}

	// Configure for production.
	api.Request(t, "POST", "/v1/providers/bureau/1/configure", map[string]any{
		"environment": "production", "config": map[string]any{"url": "https://bureau.example"},
	}, 202, nil)

	// Deploy is refused before a passing test + approval.
	api.Request(t, "POST", "/v1/providers/bureau/1/deploy", map[string]any{
		"environment": "production",
	}, 400, nil)

	// Pass the conformance test.
	api.Request(t, "POST", "/v1/providers/bureau/1/test", map[string]any{
		"passed": true, "fixture": "sandbox", "latency_ms": 120, "details": "ok",
	}, 202, nil)

	// The installer cannot self-approve; the independent checker can.
	api.Request(t, "POST", "/v1/providers/bureau/1/approve", map[string]any{
		"request_id": "req-1", "reason": "self approval must be refused",
	}, 400, nil)
	checker := api.AddKey("checker-key", auth.APIKey{
		ID: "checker", Identity: identity.Identity{Org: "demo", Workspace: "main", Actor: "checker"},
		Scope: auth.ScopeAll, Role: auth.RoleApprover,
	})
	checker.Request(t, "POST", "/v1/providers/bureau/1/approve", map[string]any{
		"request_id": "req-1", "reason": "independent review passed",
	}, 202, nil)

	// Deploy to production.
	checker.Request(t, "POST", "/v1/providers/bureau/1/deploy", map[string]any{
		"environment": "production",
	}, 202, nil)

	// The version view reflects the deployment.
	var view struct {
		Version     int               `json:"version"`
		Tested      bool              `json:"tested"`
		Approved    bool              `json:"approved"`
		Deployments map[string]string `json:"deployments"`
	}
	api.Request(t, "GET", "/v1/providers/bureau/1", nil, 200, &view)
	if !view.Tested || !view.Approved || view.Deployments["production"] != "deployed" {
		t.Fatalf("provider view = %+v, want tested+approved+deployed", view)
	}

	// Pause and resume.
	checker.Request(t, "POST", "/v1/providers/bureau/1/pause", map[string]any{
		"environment": "production", "reason": "maintenance",
	}, 202, nil)
	checker.Request(t, "POST", "/v1/providers/bureau/1/resume", map[string]any{
		"environment": "production",
	}, 202, nil)

	// List shows the provider.
	var list struct {
		Providers []struct {
			Name string `json:"name"`
		} `json:"providers"`
	}
	api.Request(t, "GET", "/v1/providers", nil, 200, &list)
	found := false
	for _, p := range list.Providers {
		if p.Name == "bureau" {
			found = true
		}
	}
	if !found {
		t.Fatalf("providers list = %+v, want bureau", list.Providers)
	}
}
