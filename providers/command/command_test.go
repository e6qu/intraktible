// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/providers/domain"
)

func setup(t *testing.T) (*Handler, identity.Identity) {
	t.Helper()
	log := eventlog.NewMemory()
	h := NewHandler(log).WithNow(func() time.Time {
		return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	})
	return h, identity.Identity{Org: "demo", Workspace: "main", Actor: "installer"}
}

func manifest() domain.Manifest {
	return domain.Manifest{
		Connector:   "credit-bureau",
		Description: "Credit bureau provider",
		Conformance: domain.Conformance{
			Schema:          `{"type":"object","properties":{"score":{"type":"number"}}}`,
			TimeoutSeconds:  10,
			MaxRetries:      2,
			CostPerFetchUSD: 0.05,
		},
	}
}

func TestProviderLifecycleEnforcesOrderedStagesAndFourEyes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, id := setup(t)
	checker := identity.Identity{Org: "demo", Workspace: "main", Actor: "checker"}

	// Install registers version 1.
	version, _, err := h.Install(ctx, id, "bureau", manifest())
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("installed version = %d, want 1", version)
	}

	// Configure for production.
	if _, err := h.Configure(ctx, id, "bureau", 1, domain.Configuration{
		Environment: domain.EnvProduction, Config: map[string]any{"url": "https://bureau.example"},
	}); err != nil {
		t.Fatal(err)
	}

	// Deploy is refused before a passing test + approval.
	if _, err := h.Deploy(ctx, id, "bureau", 1, domain.EnvProduction); err == nil ||
		!strings.Contains(err.Error(), "approved") {
		t.Fatalf("deploy before approval must be refused, got %v", err)
	}

	// A failing test does not advance the lifecycle.
	if _, err := h.Test(ctx, id, "bureau", 1, domain.TestEvidence{Passed: false}); err == nil {
		t.Fatal("a failing test must be rejected")
	}
	// A passing test is required before approval.
	if _, err := h.Test(ctx, id, "bureau", 1, domain.TestEvidence{
		Passed: true, Fixture: "sandbox", LatencyMs: 120, Details: "conformance ok",
	}); err != nil {
		t.Fatal(err)
	}

	// The installer cannot approve their own version (four-eyes).
	if _, err := h.Approve(ctx, id, "bureau", 1, "req-1", "self approval"); err == nil ||
		!strings.Contains(err.Error(), "four-eyes") {
		t.Fatalf("self-approval must be refused, got %v", err)
	}
	// The independent checker approves.
	if _, err := h.Approve(ctx, checker, "bureau", 1, "req-1", "independent review passed"); err != nil {
		t.Fatal(err)
	}

	// Deploy is now allowed in the configured environment.
	if _, err := h.Deploy(ctx, id, "bureau", 1, domain.EnvProduction); err != nil {
		t.Fatal(err)
	}
	// Deploying in an UNCONFIGURED environment is refused.
	if _, err := h.Deploy(ctx, id, "bureau", 1, domain.EnvStaging); err == nil ||
		!strings.Contains(err.Error(), "not configured") {
		t.Fatalf("deploy to unconfigured env must be refused, got %v", err)
	}

	// Pause and resume.
	if _, err := h.Pause(ctx, id, "bureau", 1, domain.EnvProduction, "maintenance"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Resume(ctx, id, "bureau", 1, domain.EnvProduction); err != nil {
		t.Fatal(err)
	}

	// Install v2, approve it, and upgrade production from v1 to v2.
	v2, _, err := h.Install(ctx, id, "bureau", manifest())
	if err != nil {
		t.Fatal(err)
	}
	if v2 != 2 {
		t.Fatalf("second install = %d, want 2", v2)
	}
	if _, err := h.Configure(ctx, id, "bureau", 2, domain.Configuration{
		Environment: domain.EnvProduction, Config: map[string]any{"url": "https://bureau-v2.example"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Test(ctx, id, "bureau", 2, domain.TestEvidence{
		Passed: true, Fixture: "sandbox", Details: "v2 conformance ok",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Approve(ctx, checker, "bureau", 2, "req-2", "v2 review passed"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Upgrade(ctx, id, "bureau", 2, domain.EnvProduction); err != nil {
		t.Fatal(err)
	}

	// Retire the v2 in production.
	if _, err := h.Retire(ctx, id, "bureau", 2, domain.EnvProduction, "sunset"); err != nil {
		t.Fatal(err)
	}
	// Re-retiring is refused.
	if _, err := h.Retire(ctx, id, "bureau", 2, domain.EnvProduction, "sunset"); err == nil {
		t.Fatal("re-retiring must be refused")
	}
}

func TestManifestValidationFailsLoudly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, id := setup(t)

	bad := manifest()
	bad.Conformance.Schema = ""
	if _, _, err := h.Install(ctx, id, "bureau", bad); err == nil {
		t.Fatal("a manifest without a schema must be rejected")
	}
	bad = manifest()
	bad.Conformance.TimeoutSeconds = 0
	if _, _, err := h.Install(ctx, id, "bureau", bad); err == nil {
		t.Fatal("a manifest without a timeout must be rejected")
	}
	bad = manifest()
	bad.Connector = ""
	if _, _, err := h.Install(ctx, id, "bureau", bad); err == nil {
		t.Fatal("a manifest without a connector must be rejected")
	}
}
