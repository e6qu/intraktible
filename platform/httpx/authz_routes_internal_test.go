// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// authorizedRoutes pins the role every registered /v1 route requires.
//
// requiredRole decides by matching path substrings, with two blanket defaults:
// every GET is a viewer read, and every other unmatched method is operator. Those
// defaults are what make the table necessary. A new endpoint inherits an
// authorization level from them silently — add a GET that returns something
// sensitive and it is readable by the lowest role in the system, with nothing in
// review drawing attention to it, because no authorization code was written.
//
// TestEveryRouteHasAPinnedRole walks the repository for route registrations and
// fails if any is absent here. The failure is the point: it makes adding a route a
// deliberate authorization decision rather than an inherited one, and it turns a
// change in the matching rules into a visible diff of exactly which endpoints moved.
var authorizedRoutes = []struct{ method, path, role string }{
	{"GET", "/v1/adverse-actions", "viewer"},
	{"GET", "/v1/agent-runs", "viewer"},
	{"GET", "/v1/agent-runs/summary", "viewer"},
	{"GET", "/v1/agent-runs/{run_id}", "viewer"},
	{"POST", "/v1/agent-runs/{run_id}/cancel", "operator"},
	{"POST", "/v1/agent-runs/{run_id}/retry", "operator"},
	{"GET", "/v1/agents", "viewer"},
	{"POST", "/v1/agents", "editor"},
	{"GET", "/v1/agents/{name}", "viewer"},
	{"GET", "/v1/agents/{name}/evals", "viewer"},
	{"PUT", "/v1/agents/{name}/evals", "operator"},
	{"POST", "/v1/agents/{name}/evals/run", "operator"},
	{"POST", "/v1/agents/{name}/run", "operator"},
	{"GET", "/v1/agents/{name}/run/stream", "operator"},
	{"GET", "/v1/agents/{name}/run/ws", "operator"},
	{"GET", "/v1/agents/{name}/runs", "viewer"},
	{"POST", "/v1/agents/{name}/runs/{run_id}/escalate", "operator"},
	{"GET", "/v1/agents/{name}/versions", "viewer"},
	{"GET", "/v1/api-keys", "admin"},
	{"POST", "/v1/api-keys", "admin"},
	{"DELETE", "/v1/api-keys/{key_id}", "admin"},
	{"POST", "/v1/api-keys/{key_id}/rotate", "admin"},
	{"GET", "/v1/audit", "admin"},
	{"GET", "/v1/auth/oidc/providers", "viewer"},
	{"POST", "/v1/auth/oidc/{provider}/backchannel-logout", "operator"},
	{"GET", "/v1/auth/oidc/{provider}/callback", "viewer"},
	{"GET", "/v1/auth/oidc/{provider}/frontchannel-logout", "viewer"},
	{"GET", "/v1/auth/oidc/{provider}/login", "viewer"},
	{"GET", "/v1/auth/saml/providers", "viewer"},
	{"POST", "/v1/auth/saml/{provider}/acs", "operator"},
	{"GET", "/v1/auth/saml/{provider}/login", "viewer"},
	{"GET", "/v1/auth/saml/{provider}/metadata", "viewer"},
	{"GET", "/v1/auth/signed-out", "viewer"},
	{"GET", "/v1/auth/signed-out.css", "viewer"},
	{"GET", "/v1/cases", "viewer"},
	{"POST", "/v1/cases", "operator"},
	{"POST", "/v1/cases/sla-sweep", "operator"},
	{"GET", "/v1/cases/summary", "viewer"},
	{"GET", "/v1/cases/{case_id}", "viewer"},
	{"POST", "/v1/cases/{case_id}/assign", "operator"},
	{"POST", "/v1/cases/{case_id}/notes", "operator"},
	{"POST", "/v1/cases/{case_id}/status", "operator"},
	{"GET", "/v1/comments/{subject_type}/{subject_id}", "viewer"},
	{"POST", "/v1/comments/{subject_type}/{subject_id}", "operator"},
	{"GET", "/v1/compliance/jurisdiction", "viewer"},
	{"PUT", "/v1/compliance/jurisdiction", "admin"},
	{"GET", "/v1/compliance/registers/{register}", "viewer"},
	{"GET", "/v1/consent", "viewer"},
	{"POST", "/v1/consent/grant", "operator"},
	{"GET", "/v1/consent/records", "viewer"},
	{"GET", "/v1/consent/status", "viewer"},
	{"POST", "/v1/consent/withdraw", "operator"},
	{"GET", "/v1/contests", "viewer"},
	{"GET", "/v1/context/connectors", "viewer"},
	{"POST", "/v1/context/connectors", "editor"},
	{"GET", "/v1/context/connectors/catalog", "viewer"},
	{"POST", "/v1/context/connectors/{name}/fetch", "operator"},
	{"GET", "/v1/context/connectors/{name}/fetches", "viewer"},
	{"GET", "/v1/context/entities", "viewer"},
	{"POST", "/v1/context/entities", "operator"},
	{"GET", "/v1/context/entities/{type}/{id}", "viewer"},
	{"GET", "/v1/context/entities/{type}/{id}/events", "viewer"},
	{"GET", "/v1/context/entities/{type}/{id}/features", "viewer"},
	{"POST", "/v1/context/events", "operator"},
	{"GET", "/v1/context/features", "viewer"},
	{"POST", "/v1/context/features", "editor"},
	{"POST", "/v1/copilot/explain", "operator"},
	{"POST", "/v1/copilot/generate", "operator"},
	{"POST", "/v1/copilot/suggest", "operator"},
	{"GET", "/v1/decisions", "viewer"},
	{"GET", "/v1/decisions/summary", "viewer"},
	{"GET", "/v1/decisions/{decision_id}", "viewer"},
	{"GET", "/v1/decisions/{decision_id}/adverse-action", "operator"},
	{"GET", "/v1/decisions/{decision_id}/adverse-action/issued", "operator"},
	{"POST", "/v1/decisions/{decision_id}/adverse-action/issue", "operator"},
	{"GET", "/v1/decisions/{decision_id}/contest", "viewer"},
	{"POST", "/v1/decisions/{decision_id}/contest", "operator"},
	{"POST", "/v1/decisions/{decision_id}/counterfactual", "operator"},
	{"GET", "/v1/decisions/{decision_id}/explanation", "viewer"},
	{"GET", "/v1/decisions/{decision_id}/export", "viewer"},
	{"GET", "/v1/decisions/{decision_id}/reconsideration", "viewer"},
	{"POST", "/v1/decisions/{decision_id}/reconsideration", "operator"},
	{"POST", "/v1/decisions/{decision_id}/resume", "operator"},
	{"GET", "/v1/erasure/holds", "admin"},
	{"POST", "/v1/erasure/retention", "admin"},
	{"GET", "/v1/erasure/retention-policy", "admin"},
	{"PUT", "/v1/erasure/retention-policy", "admin"},
	{"GET", "/v1/erasure/subjects", "admin"},
	{"GET", "/v1/erasure/subjects/{subject}", "admin"},
	{"POST", "/v1/erasure/subjects/{subject}", "admin"},
	{"POST", "/v1/erasure/subjects/{subject}/hold", "admin"},
	{"POST", "/v1/erasure/subjects/{subject}/release", "admin"},
	{"GET", "/v1/experiments", "viewer"},
	{"POST", "/v1/experiments", "editor"},
	{"GET", "/v1/experiments/{experiment_id}", "viewer"},
	{"PUT", "/v1/experiments/{experiment_id}", "editor"},
	{"GET", "/v1/experiments/{experiment_id}/analysis", "viewer"},
	{"POST", "/v1/experiments/{experiment_id}/cancel", "operator"},
	{"POST", "/v1/experiments/{experiment_id}/complete", "operator"},
	{"GET", "/v1/experiments/{experiment_id}/exposures", "viewer"},
	{"POST", "/v1/experiments/{experiment_id}/launch-requests/{request_id}/approve", "approver"},
	{"POST", "/v1/experiments/{experiment_id}/launch-requests/{request_id}/reject", "approver"},
	{"POST", "/v1/experiments/{experiment_id}/pause", "operator"},
	{"POST", "/v1/experiments/{experiment_id}/promote", "approver"},
	{"POST", "/v1/experiments/{experiment_id}/resume", "operator"},
	{"POST", "/v1/experiments/{experiment_id}/start", "editor"},
	{"GET", "/v1/fairlending/report", "admin"},
	{"GET", "/v1/fairlending/settings", "admin"},
	{"PUT", "/v1/fairlending/settings", "admin"},
	{"GET", "/v1/flows", "viewer"},
	{"POST", "/v1/flows", "editor"},
	{"POST", "/v1/flows/import", "editor"},
	{"POST", "/v1/flows/import-bundle", "editor"},
	{"GET", "/v1/flows/{flow_id}", "viewer"},
	{"PATCH", "/v1/flows/{flow_id}", "editor"},
	{"GET", "/v1/flows/{flow_id}/assertions", "viewer"},
	{"PUT", "/v1/flows/{flow_id}/assertions", "editor"},
	{"POST", "/v1/flows/{flow_id}/assertions/run", "operator"},
	{"POST", "/v1/flows/{flow_id}/backtest", "operator"},
	{"POST", "/v1/flows/{flow_id}/baseline", "operator"},
	{"POST", "/v1/flows/{flow_id}/coverage", "operator"},
	{"POST", "/v1/flows/{flow_id}/deployment-requests", "editor"},
	{"POST", "/v1/flows/{flow_id}/deployment-requests/{req_id}/approve", "approver"},
	{"POST", "/v1/flows/{flow_id}/deployment-requests/{req_id}/reject", "approver"},
	{"POST", "/v1/flows/{flow_id}/deployments", "approver"},
	{"POST", "/v1/flows/{flow_id}/deployments/rollback", "approver"},
	{"POST", "/v1/flows/{flow_id}/deployments/schedule", "approver"},
	{"GET", "/v1/flows/{flow_id}/deployments/schedules", "viewer"},
	{"DELETE", "/v1/flows/{flow_id}/deployments/schedules/{schedule_id}", "approver"},
	{"GET", "/v1/flows/{flow_id}/drift", "viewer"},
	{"GET", "/v1/flows/{flow_id}/export", "viewer"},
	{"GET", "/v1/flows/{flow_id}/fairlending", "viewer"},
	{"PUT", "/v1/flows/{flow_id}/fairlending", "admin"},
	{"GET", "/v1/flows/{flow_id}/grants", "admin"},
	{"POST", "/v1/flows/{flow_id}/grants", "admin"},
	{"DELETE", "/v1/flows/{flow_id}/grants/{grant_id}", "admin"},
	{"GET", "/v1/flows/{flow_id}/metrics", "viewer"},
	{"GET", "/v1/flows/{flow_id}/monitors", "viewer"},
	{"POST", "/v1/flows/{flow_id}/monitors", "editor"},
	{"POST", "/v1/flows/{flow_id}/monitors/check", "editor"},
	{"DELETE", "/v1/flows/{flow_id}/monitors/{monitor_id}", "editor"},
	{"GET", "/v1/flows/{flow_id}/node-stats", "viewer"},
	{"POST", "/v1/flows/{flow_id}/promote", "approver"},
	{"GET", "/v1/flows/{flow_id}/promotion-policy", "viewer"},
	{"PUT", "/v1/flows/{flow_id}/promotion-policy", "approver"},
	{"GET", "/v1/flows/{flow_id}/shadow", "viewer"},
	{"PUT", "/v1/flows/{flow_id}/shadow", "editor"},
	{"GET", "/v1/flows/{flow_id}/slo", "viewer"},
	{"PUT", "/v1/flows/{flow_id}/slo", "editor"},
	{"POST", "/v1/flows/{flow_id}/versions", "editor"},
	{"POST", "/v1/flows/{flow_id}/whatif", "operator"},
	{"GET", "/v1/flows/{slug}/openapi.json", "viewer"},
	{"POST", "/v1/flows/{slug}/{env}/decide", "operator"},
	{"POST", "/v1/flows/{slug}/{env}/decide/batch", "operator"},
	{"POST", "/v1/flows/{slug}/{env}/decide/stream", "operator"},
	{"POST", "/v1/flows/{slug}/{env}/preapprove/batch", "editor"},
	{"POST", "/v1/hello", "operator"},
	{"GET", "/v1/hello/stats", "viewer"},
	{"POST", "/v1/login", "operator"},
	{"POST", "/v1/logout", "operator"},
	{"GET", "/v1/me", "viewer"},
	{"GET", "/v1/models", "viewer"},
	{"POST", "/v1/models", "editor"},
	{"POST", "/v1/models/train", "editor"},
	{"GET", "/v1/models/{name}", "viewer"},
	{"POST", "/v1/models/{name}/approval-request", "editor"},
	{"POST", "/v1/models/{name}/approve", "approver"},
	{"POST", "/v1/models/{name}/baseline", "editor"},
	{"GET", "/v1/models/{name}/drift", "viewer"},
	{"POST", "/v1/models/{name}/monitor", "editor"},
	{"POST", "/v1/models/{name}/outcomes", "operator"},
	{"GET", "/v1/models/{name}/performance", "viewer"},
	{"POST", "/v1/models/{name}/reject", "approver"},
	{"POST", "/v1/models/{name}/validation", "approver"},
	{"GET", "/v1/mrm/report", "admin"},
	{"GET", "/v1/notifications", "viewer"},
	{"POST", "/v1/notifications/{notification_id}/read", "viewer"},
	{"GET", "/v1/outcomes", "viewer"},
	{"POST", "/v1/outcomes", "operator"},
	{"GET", "/v1/outcomes/{outcome_id}", "viewer"},
	{"POST", "/v1/outcomes/{outcome_id}/corrections", "operator"},
	{"GET", "/v1/policies", "viewer"},
	{"POST", "/v1/policies", "editor"},
	{"GET", "/v1/policies/{policy_id}", "viewer"},
	{"POST", "/v1/policies/{policy_id}/approval-request", "editor"},
	{"POST", "/v1/policies/{policy_id}/approve", "approver"},
	{"POST", "/v1/policies/{policy_id}/backtest", "operator"},
	{"POST", "/v1/policies/{policy_id}/reject", "approver"},
	{"POST", "/v1/policies/{policy_id}/versions", "editor"},
	{"GET", "/v1/preapprovals", "viewer"},
	{"POST", "/v1/preapprovals", "editor"},
	{"GET", "/v1/preapprovals/{type}/{id}", "viewer"},
	{"POST", "/v1/preapprovals/{type}/{id}/revoke", "operator"},
	{"GET", "/v1/population-jobs", "viewer"},
	{"POST", "/v1/population-jobs", "operator"},
	{"GET", "/v1/population-jobs/{job_id}", "viewer"},
	{"POST", "/v1/population-jobs/{job_id}/cancel", "operator"},
	{"POST", "/v1/population-jobs/{job_id}/pause", "operator"},
	{"GET", "/v1/population-jobs/{job_id}/results", "viewer"},
	{"POST", "/v1/population-jobs/{job_id}/resume", "operator"},
	{"POST", "/v1/population-jobs/{job_id}/retry", "operator"},
	{"GET", "/v1/privacy", "viewer"},
	{"PUT", "/v1/privacy", "admin"},
	{"GET", "/v1/reconsiderations", "viewer"},
	{"GET", "/v1/retention", "viewer"},
	{"GET", "/v1/sharing", "viewer"},
	{"POST", "/v1/sharing/opt-in", "operator"},
	{"POST", "/v1/sharing/opt-out", "operator"},
	{"GET", "/v1/sharing/records", "viewer"},
	{"GET", "/v1/webhooks", "viewer"},
	{"POST", "/v1/webhooks", "editor"},
	{"DELETE", "/v1/webhooks/{webhook_id}", "editor"},
	{"POST", "/v1/x/{id}", "operator"},
}

// routeRegistration matches both ways this repository mounts a route:
//
//	mux.HandleFunc("GET /v1/flows/{flow_id}", h)
//	{Method: "GET", Pattern: "/v1/flows/{flow_id}", Handler: h}
//
// Both are load-bearing. An earlier audit pass scanned only the first and concluded
// nine documented endpoints did not exist.
var routeRegistration = regexp.MustCompile(
	`"(GET|POST|PUT|PATCH|DELETE) (/v1/[^"]*)"|Method: *"(GET|POST|PUT|PATCH|DELETE)", *Pattern: *"(/v1/[^"]*)"`)

// repoRoutes returns every /v1 route registered anywhere in the repository.
func repoRoutes(t *testing.T) map[string]bool {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// node_modules ships vendored Go files that are not ours.
			if d.Name() == "node_modules" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path) // #nosec G304 -- walking this repository's own source
		if err != nil {
			return err
		}
		for _, m := range routeRegistration.FindAllStringSubmatch(string(src), -1) {
			method, route := m[1], m[2]
			if method == "" {
				method, route = m[3], m[4]
			}
			found[method+" "+route] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 {
		t.Fatal("found no route registrations — the scan is broken, not the routes")
	}
	return found
}

func TestEveryRouteHasAPinnedRole(t *testing.T) {
	pinned := map[string]string{}
	for _, r := range authorizedRoutes {
		key := r.method + " " + r.path
		if _, dup := pinned[key]; dup {
			t.Errorf("%s is pinned twice", key)
		}
		pinned[key] = r.role
	}

	registered := repoRoutes(t)

	var unpinned []string
	for route := range registered {
		if _, ok := pinned[route]; !ok {
			unpinned = append(unpinned, route)
		}
	}
	sort.Strings(unpinned)
	for _, route := range unpinned {
		parts := strings.SplitN(route, " ", 2)
		t.Errorf("route %s is registered but not pinned in authorizedRoutes; it would inherit %q from the blanket default — add it deliberately",
			route, requiredRole(parts[0], parts[1]))
	}

	var stale []string
	for route := range pinned {
		if !registered[route] {
			stale = append(stale, route)
		}
	}
	sort.Strings(stale)
	for _, route := range stale {
		t.Errorf("route %s is pinned but no longer registered; remove it from authorizedRoutes", route)
	}
}

// TestPinnedRolesAreUnchanged is the regression half: it fails when a change to the
// matching rules silently moves an existing endpoint to a different role.
func TestPinnedRolesAreUnchanged(t *testing.T) {
	for _, r := range authorizedRoutes {
		if got := string(requiredRole(r.method, r.path)); got != r.role {
			t.Errorf("%s %s requires %q, pinned as %q", r.method, r.path, got, r.role)
		}
	}
}
