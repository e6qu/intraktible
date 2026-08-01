// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/packs/domain"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

func setup(t *testing.T) (*Handler, identity.Identity) {
	t.Helper()
	log := eventlog.NewMemory()
	h := NewHandler(log).WithNow(func() time.Time {
		return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	})
	return h, identity.Identity{Org: "demo", Workspace: "main", Actor: "owner"}
}

func manifest(upgradeFrom ...int) domain.Manifest {
	return domain.Manifest{
		Title:       "Credit origination pack",
		Description: "Complete credit origination journey.",
		Signature:   "sig",
		Artifacts: []domain.Artifact{
			{Kind: domain.ArtifactFlow, ID: "credit-stp", Content: map[string]any{"graph": "..."}},
			{Kind: domain.ArtifactPolicy, ID: "credit-policy", Content: map[string]any{"bands": "..."}},
			{Kind: domain.ArtifactCaseType, ID: "aml", Content: map[string]any{"states": "..."}},
		},
		UpgradeFrom: upgradeFrom,
	}
}

func TestPackLifecycleInstallUpgradeRollbackRetire(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, id := setup(t)

	// Define v1 and install it.
	v1, _, err := h.Define(ctx, id, "credit", manifest())
	if err != nil {
		t.Fatal(err)
	}
	if v1 != 1 {
		t.Fatalf("defined version = %d, want 1", v1)
	}
	if _, err := h.Install(ctx, id, "credit", 1); err != nil {
		t.Fatal(err)
	}
	// Re-installing the same version is refused.
	if _, err := h.Install(ctx, id, "credit", 1); err == nil {
		t.Fatal("re-installing the same version must be refused")
	}

	// Define v2 (upgrades from v1) — installing it directly over the installed v1
	// is refused (use upgrade), then upgrade works.
	v2, _, err := h.Define(ctx, id, "credit", manifest(1))
	if err != nil {
		t.Fatal(err)
	}
	if v2 != 2 {
		t.Fatalf("second define = %d, want 2", v2)
	}
	if _, err := h.Install(ctx, id, "credit", 2); err == nil ||
		!strings.Contains(err.Error(), "upgrade or roll back") {
		t.Fatalf("install over an installed version must be refused, got %v", err)
	}
	if _, err := h.Upgrade(ctx, id, "credit", 2); err != nil {
		t.Fatal(err)
	}

	// Roll back to v1.
	if _, err := h.Rollback(ctx, id, "credit", 1); err != nil {
		t.Fatal(err)
	}
	// Rolling back to a non-older version is refused.
	if _, err := h.Rollback(ctx, id, "credit", 2); err == nil {
		t.Fatal("rollback to a newer version must be refused")
	}

	// Retire.
	if _, err := h.Retire(ctx, id, "credit", "sunset"); err != nil {
		t.Fatal(err)
	}
	// Retiring a non-installed pack is refused.
	if _, err := h.Retire(ctx, id, "credit", "sunset"); err == nil {
		t.Fatal("retiring a non-installed pack must be refused")
	}
}

func TestPackDependencyEnforcement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, id := setup(t)

	// Define a base pack and install it.
	if _, _, err := h.Define(ctx, id, "base", manifest()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Install(ctx, id, "base", 1); err != nil {
		t.Fatal(err)
	}

	// A dependent pack requiring base@1 installs.
	dep := manifest()
	dep.Dependencies = []domain.Dependency{{Kind: "pack", Name: "base", Version: 1}}
	if _, _, err := h.Define(ctx, id, "dependent", dep); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Install(ctx, id, "dependent", 1); err != nil {
		t.Fatal(err)
	}

	// A dependent pack requiring base@2 (not installed) is refused.
	dep2 := manifest()
	dep2.Dependencies = []domain.Dependency{{Kind: "pack", Name: "base", Version: 2}}
	if _, _, err := h.Define(ctx, id, "dependent2", dep2); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Install(ctx, id, "dependent2", 1); err == nil ||
		!strings.Contains(err.Error(), "installed at version 2") {
		t.Fatalf("an unsatisfied pack dependency must be refused, got %v", err)
	}
}

func TestPackManifestValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, id := setup(t)

	bad := manifest()
	bad.Signature = ""
	if _, _, err := h.Define(ctx, id, "credit", bad); err == nil {
		t.Fatal("an unsigned manifest must be rejected")
	}
	bad = manifest()
	bad.Artifacts = nil
	if _, _, err := h.Define(ctx, id, "credit", bad); err == nil {
		t.Fatal("an artifact-less manifest must be rejected")
	}
	bad = manifest()
	bad.Artifacts = append(bad.Artifacts, bad.Artifacts[0])
	if _, _, err := h.Define(ctx, id, "credit", bad); err == nil {
		t.Fatal("duplicate artifacts must be rejected")
	}
	bad = manifest()
	bad.Dependencies = []domain.Dependency{{Kind: "weird", Name: "x", Version: 1}}
	if _, _, err := h.Define(ctx, id, "credit", bad); err == nil {
		t.Fatal("an unknown dependency kind must be rejected")
	}
}
