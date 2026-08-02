// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
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
