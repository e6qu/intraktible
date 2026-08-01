// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"testing"

	"github.com/e6qu/intraktible/packs/command"
	"github.com/e6qu/intraktible/packs/projection"
	"github.com/e6qu/intraktible/packs/service"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
)

func startPacks(t *testing.T) *testutil.API {
	log, st := testutil.NewLogStore(t)
	svc := service.New(command.NewHandler(log), st)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "owner"}
	return testutil.StartAPI(t, log, st, "test-key", id, svc.Routes, projection.Projector{})
}

func manifest(upgradeFrom ...int) map[string]any {
	return map[string]any{
		"name":        "credit",
		"title":       "Credit origination pack",
		"description": "Complete credit origination journey.",
		"signature":   "sig",
		"artifacts": []map[string]any{
			{"kind": "flow", "id": "credit-stp", "content": map[string]any{"graph": "..."}},
			{"kind": "policy", "id": "credit-policy", "content": map[string]any{"bands": "..."}},
		},
		"upgrade_from": upgradeFrom,
	}
}

func TestPackLifecycleOverHTTP(t *testing.T) {
	api := startPacks(t)

	// Define and install v1.
	var defined struct {
		Version int `json:"version"`
	}
	api.Request(t, "POST", "/v1/packs", manifest(), 202, &defined)
	if defined.Version != 1 {
		t.Fatalf("defined version = %d, want 1", defined.Version)
	}
	api.Request(t, "POST", "/v1/packs/credit/install", map[string]any{"version": 1}, 202, nil)

	// Re-installing the same version is refused.
	api.Request(t, "POST", "/v1/packs/credit/install", map[string]any{"version": 1}, 400, nil)

	// Define v2 (upgrades from v1) and upgrade.
	api.Request(t, "POST", "/v1/packs", manifest(1), 202, nil)
	api.Request(t, "POST", "/v1/packs/credit/upgrade", map[string]any{"version": 2}, 202, nil)

	// The pack view reflects the upgrade.
	var view struct {
		Installed int  `json:"installed"`
		Retired   bool `json:"retired"`
	}
	api.Request(t, "GET", "/v1/packs/credit", nil, 200, &view)
	if view.Installed != 2 || view.Retired {
		t.Fatalf("pack view = %+v, want installed=2 not retired", view)
	}

	// Roll back to v1.
	api.Request(t, "POST", "/v1/packs/credit/rollback", map[string]any{"version": 1}, 202, nil)
	api.Request(t, "GET", "/v1/packs/credit", nil, 200, &view)
	if view.Installed != 1 {
		t.Fatalf("after rollback installed = %d, want 1", view.Installed)
	}

	// Retire.
	api.Request(t, "POST", "/v1/packs/credit/retire", map[string]any{"reason": "sunset"}, 202, nil)
	api.Request(t, "GET", "/v1/packs/credit", nil, 200, &view)
	if !view.Retired || view.Installed != 0 {
		t.Fatalf("after retire view = %+v, want retired not installed", view)
	}

	// List shows the pack.
	var list struct {
		Packs []struct {
			Name string `json:"name"`
		} `json:"packs"`
	}
	api.Request(t, "GET", "/v1/packs", nil, 200, &list)
	found := false
	for _, p := range list.Packs {
		if p.Name == "credit" {
			found = true
		}
	}
	if !found {
		t.Fatalf("packs list = %+v, want credit", list.Packs)
	}
}
