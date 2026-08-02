// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/e6qu/intraktible/server"
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

	// The server's own preflight, not a copy of its rules: a second
	// implementation would drift, pass here, and still refuse to boot in the
	// deployment — the exact failure this gate exists to prevent.
	cfg := server.Config{Env: *env, StoreKind: *storeKind, LogKind: *logKind}
	encryptionEnabled := strings.TrimSpace(os.Getenv("INTRAKTIBLE_ENCRYPTION_KEY")) != ""
	if err := server.CheckProductionConfig(cfg, encryptionEnabled); err != nil {
		return err
	}

	if !strings.EqualFold(*env, "production") {
		fmt.Println("OK: non-production environment — preflight is a no-op")
		return nil
	}

	// Non-fatal: a production deployment with no admin credential at all boots
	// happily and cannot be administered.
	var advisories []string
	if strings.TrimSpace(os.Getenv("INTRAKTIBLE_BOOTSTRAP_API_KEY")) == "" &&
		strings.TrimSpace(os.Getenv("INTRAKTIBLE_OIDC_SHAUTH_CLIENT_ID")) == "" {
		advisories = append(advisories, "Neither INTRAKTIBLE_BOOTSTRAP_API_KEY nor SSO is configured — no admin credential available")
	}

	fmt.Println("OK: production config check passed")
	for _, a := range advisories {
		fmt.Printf("ADVISORY: %s\n", a)
	}
	return nil
}
