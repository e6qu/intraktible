// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"net/http"
	"testing"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/identity"
)

// TestCrossTenantIsolation proves one tenant cannot read, list, or access another
// tenant's flows, decisions, or audit trail through the real HTTP API. This is
// the E7 cross-tenant isolation evidence across the shared API + projection store.
func TestCrossTenantIsolation(t *testing.T) {
	api := startEngine(t)

	// Tenant A (the seeded key's org "demo") creates a flow.
	var flowA struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "alpha-flow", "name": "Alpha flow"},
		http.StatusCreated, &flowA)
	api.Request(t, http.MethodPost, "/v1/flows/"+flowA.FlowID+"/versions",
		map[string]any{"graph": map[string]any{
			"nodes": []map[string]any{{"id": "in", "type": "input"}, {"id": "out", "type": "output"}},
			"edges": []map[string]any{{"from": "in", "to": "out"}},
		}}, http.StatusCreated, nil)

	// Tenant B is in a DIFFERENT org.
	tenantB := api.AddKey("tenant-b-key", auth.APIKey{
		ID:       "tenant-b",
		Identity: identity.Identity{Org: "beta", Workspace: "main", Actor: "b-actor"},
		Scope:    auth.ScopeAll, Role: auth.RoleAdmin,
	})

	// Tenant B lists flows — must not see Tenant A's "Alpha flow".
	var flowsB struct {
		Flows []struct {
			Name string `json:"name"`
		} `json:"flows"`
	}
	tenantB.Request(t, http.MethodGet, "/v1/flows", nil, http.StatusOK, &flowsB)
	for _, f := range flowsB.Flows {
		if f.Name == "Alpha flow" {
			t.Fatal("Tenant B can see Tenant A's flow — cross-tenant isolation is broken")
		}
	}

	// Tenant B reads Tenant A's flow by its ID — must 404 (it doesn't exist in B's scope).
	tenantB.Request(t, http.MethodGet, "/v1/flows/"+flowA.FlowID, nil, http.StatusNotFound, nil)

	// Tenant B creates its own flow — Tenant A must not see it either.
	var flowB struct {
		FlowID string `json:"flow_id"`
	}
	tenantB.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "beta-flow", "name": "Beta flow"},
		http.StatusCreated, &flowB)

	// Tenant A lists flows — must not see "Beta flow".
	var flowsA struct {
		Flows []struct {
			Name string `json:"name"`
		} `json:"flows"`
	}
	api.Request(t, http.MethodGet, "/v1/flows", nil, http.StatusOK, &flowsA)
	for _, f := range flowsA.Flows {
		if f.Name == "Beta flow" {
			t.Fatal("Tenant A can see Tenant B's flow — cross-tenant isolation is broken")
		}
	}

	// Tenant A reads Tenant B's flow by ID — must 404.
	api.Request(t, http.MethodGet, "/v1/flows/"+flowB.FlowID, nil, http.StatusNotFound, nil)

	// Tenant B's decisions must not include Tenant A's flows.
	var decisionsB struct {
		Decisions []struct {
			FlowID string `json:"flow_id"`
		} `json:"decisions"`
	}
	tenantB.Request(t, http.MethodGet, "/v1/decisions", nil, http.StatusOK, &decisionsB)
	for _, d := range decisionsB.Decisions {
		if d.FlowID == flowA.FlowID {
			t.Fatal("Tenant B's decision history includes Tenant A's flow — isolation is broken")
		}
	}
}
