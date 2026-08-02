// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"
)

func TestCheckConfigFailsWithoutRequiredVars(t *testing.T) {
	t.Setenv("INTRAKTIBLE_ENV", "production")
	t.Setenv("INTRAKTIBLE_ENCRYPTION_KEY", "")
	t.Setenv("INTRAKTIBLE_ARTIFACT_SIGNING_KEY", "")

	err := checkConfigCmd([]string{"--env=production", "--store=postgres", "--log=postgres"})
	if err == nil {
		t.Fatal("production config check must fail without required vars")
	}
}

func TestCheckConfigPassesWithAllRequiredVars(t *testing.T) {
	t.Setenv("INTRAKTIBLE_ENV", "production")
	t.Setenv("INTRAKTIBLE_ENCRYPTION_KEY", "dGVzdC1rZXktMzItYnl0ZXMtbG9uZy1lbg==")
	t.Setenv("INTRAKTIBLE_ARTIFACT_SIGNING_KEY", "dGVzdC1rZXktMzItYnl0ZXMtbG9uZy1lbg==")
	t.Setenv("INTRAKTIBLE_POSTGRES_DSN", "postgres://test:test@localhost:5432/test")

	if err := checkConfigCmd([]string{"--env=production", "--store=postgres", "--log=postgres"}); err != nil {
		t.Fatalf("production config check with all vars set should pass, got: %v", err)
	}
}

func TestCheckConfigNonProductionIsNoop(t *testing.T) {
	if err := checkConfigCmd([]string{"--env=development"}); err != nil {
		t.Fatalf("non-production check should be a no-op, got: %v", err)
	}
}

func TestPrintRequiredListsAllRequiredVars(t *testing.T) {
	if err := checkConfigCmd([]string{"--print-required"}); err != nil {
		t.Fatalf("--print-required should succeed, got: %v", err)
	}
}

// TestCheckConfigReportsTheServersOwnRefusal is the point of the gate: the
// command must fail with the very message the server fails to boot with, not
// with a second implementation's paraphrase of it. A copy would drift, pass
// here, and still refuse to boot in the deployment — which is how
// INTRAKTIBLE_ARTIFACT_SIGNING_KEY reached production and took the site down
// (e6qu/infra#163).
func TestCheckConfigReportsTheServersOwnRefusal(t *testing.T) {
	t.Setenv("INTRAKTIBLE_ENV", "production")
	t.Setenv("INTRAKTIBLE_ENCRYPTION_KEY", "dGVzdC1rZXktMzItYnl0ZXMtbG9uZy1lbg==")
	t.Setenv("INTRAKTIBLE_ARTIFACT_SIGNING_KEY", "")

	err := checkConfigCmd([]string{"--env=production", "--store=postgres", "--log=postgres"})
	if err == nil {
		t.Fatal("a missing artifact signing key must fail the check")
	}
	want := "server: refusing to start with INTRAKTIBLE_ENV=production"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("check reported %q, which does not carry the server's own refusal (%q)", err.Error(), want)
	}
	if !strings.Contains(err.Error(), "INTRAKTIBLE_ARTIFACT_SIGNING_KEY") {
		t.Fatalf("check did not name the missing variable: %q", err.Error())
	}
}

// Every variable the command advertises as required must actually be required:
// an entry that the preflight does not enforce is documentation that lies.
func TestRequiredProductionVarsAreEnforced(t *testing.T) {
	complete := map[string]string{
		"INTRAKTIBLE_ENV":                  "production",
		"INTRAKTIBLE_ENCRYPTION_KEY":       "dGVzdC1rZXktMzItYnl0ZXMtbG9uZy1lbg==",
		"INTRAKTIBLE_ARTIFACT_SIGNING_KEY": "dGVzdC1rZXktMzItYnl0ZXMtbG9uZy1lbg==",
	}
	for _, required := range RequiredProductionVars {
		// The environment name selects the mode rather than being enforced by
		// it, and the DSN is enforced by the store the caller selects.
		if required.Name == "INTRAKTIBLE_ENV" || required.Name == "INTRAKTIBLE_POSTGRES_DSN" {
			continue
		}
		t.Run(required.Name, func(t *testing.T) {
			for name, value := range complete {
				t.Setenv(name, value)
			}
			t.Setenv(required.Name, "")
			err := checkConfigCmd([]string{"--env=production", "--store=postgres", "--log=postgres"})
			if err == nil {
				t.Fatalf("%s is advertised as required but the preflight accepts its absence", required.Name)
			}
			if !strings.Contains(err.Error(), required.Name) {
				t.Fatalf("the refusal for a missing %s does not name it: %q", required.Name, err.Error())
			}
		})
	}
}
