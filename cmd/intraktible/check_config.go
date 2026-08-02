// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

// RequiredProductionVars is the single source of truth for environment
// variables that a production deployment MUST set. A deployer can diff this
// list between releases with `intraktible check-config --print-required`.
var RequiredProductionVars = []struct {
	Name        string
	Description string
}{
	{"INTRAKTIBLE_ENV", "Set to 'production' to enable secure-cookie, preflight, and fail-closed behavior."},
	{"INTRAKTIBLE_ENCRYPTION_KEY", "32-byte key for AES-256-GCM encryption at rest (event payloads, PII). Generate: openssl rand -base64 32."},
	{"INTRAKTIBLE_ARTIFACT_SIGNING_KEY", "32-byte Ed25519 seed for stable artifact-signing identity across worker replicas. Generate: openssl rand -base64 32."},
	{"INTRAKTIBLE_POSTGRES_DSN", "PostgreSQL connection string for the event log and/or projection store (production uses --log=postgres)."},
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// checkConfigCmd validates the configuration without booting the server. With
// --print-required it lists the environment variables a production deployment
// must set (the deployment contract). With --env=production it runs the same
// preflight checks the server would run, so a CI pipeline can catch a missing
// variable before the image is deployed.
func checkConfigCmd(args []string) error {
	fs := flag.NewFlagSet("check-config", flag.ExitOnError)
	printRequired := fs.Bool("print-required", false, "print the required production environment variables and exit")
	env := fs.String("env", envOr("INTRAKTIBLE_ENV", "development"), "deployment environment to validate")
	storeKind := fs.String("store", "memory", "projection store")
	logKind := fs.String("log", "file", "event log")
	_ = fs.Parse(args)

	if *printRequired {
		fmt.Println("# Required production environment variables")
		fmt.Println("# A production deployment MUST set all of these.")
		fmt.Println()
		for _, v := range RequiredProductionVars {
			fmt.Printf("# %s\n%s=\n\n", v.Description, v.Name)
		}
		return nil
	}

	// Run the same preflight the server runs, so a CI gate catches a missing
	// variable in the repo that introduced it, not in someone else's deployment.
	encryptionKey := strings.TrimSpace(os.Getenv("INTRAKTIBLE_ENCRYPTION_KEY"))
	encryptionEnabled := encryptionKey != ""

	cfg := struct {
		Env       string
		StoreKind string
		LogKind   string
	}{
		Env: *env, StoreKind: *storeKind, LogKind: *logKind,
	}

	if !strings.EqualFold(cfg.Env, "production") {
		fmt.Println("OK: non-production environment — preflight is a no-op")
		return nil
	}

	var problems []string
	if cfg.StoreKind == "memory" {
		problems = append(problems, "--store=memory is not durable (read models are lost on restart); use sqlite or postgres")
	}
	if cfg.LogKind == "memory" {
		problems = append(problems, "--log=memory is not durable (the event log is the system of record); use file, sqlite, postgres, or nats")
	}
	if !encryptionEnabled && !truthy(os.Getenv("INTRAKTIBLE_ALLOW_PLAINTEXT_AT_REST")) {
		problems = append(problems, "INTRAKTIBLE_ENCRYPTION_KEY is unset, so PII/event payloads would be written in plaintext at rest")
	}
	if strings.TrimSpace(os.Getenv("INTRAKTIBLE_ARTIFACT_SIGNING_KEY")) == "" {
		problems = append(problems, "INTRAKTIBLE_ARTIFACT_SIGNING_KEY is unset, so training-worker replicas would not share a stable artifact-signing identity")
	}
	if cfg.LogKind == "file" && !truthy(os.Getenv("INTRAKTIBLE_SINGLE_REPLICA")) {
		problems = append(problems, "--log=file is a single-process WAL: every replica would keep its own divergent copy of the event log")
	}

	// Warn about missing optional-but-recommended variables.
	var advisories []string
	if strings.TrimSpace(os.Getenv("INTRAKTIBLE_BOOTSTRAP_API_KEY")) == "" && strings.TrimSpace(os.Getenv("INTRAKTIBLE_OIDC_SHAUTH_CLIENT_ID")) == "" {
		advisories = append(advisories, "Neither INTRAKTIBLE_BOOTSTRAP_API_KEY nor SSO is configured — no admin credential available")
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("production config check FAILED:\n  - %s", strings.Join(problems, "\n  - "))
	}

	fmt.Println("OK: production config check passed")
	for _, a := range advisories {
		fmt.Printf("ADVISORY: %s\n", a)
	}
	return nil
}
