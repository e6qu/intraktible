// SPDX-License-Identifier: AGPL-3.0-or-later

// Package domain holds the pure provider-lifecycle model: versioned manifests,
// per-environment configuration, the install→test→approve→deploy→pause→upgrade→
// retire lifecycle, and conformance evidence. No I/O.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Stage is one provider's lifecycle stage in one environment.
type Stage string

const (
	StageInstalled  Stage = "installed"
	StageConfigured Stage = "configured"
	StageTested     Stage = "tested"
	StageApproved   Stage = "approved"
	StageDeployed   Stage = "deployed"
	StagePaused     Stage = "paused"
	StageRetired    Stage = "retired"
)

// Environment is a decision environment a provider binds to.
type Environment string

const (
	EnvSandbox    Environment = "sandbox"
	EnvStaging    Environment = "staging"
	EnvProduction Environment = "production"
)

// Valid reports whether e is a known environment.
func (e Environment) Valid() bool {
	return e == EnvSandbox || e == EnvStaging || e == EnvProduction
}

// Conformance declares the operational contract a provider version commits to:
// the typed schema it serves, idempotency, pagination, retry/backoff, circuit
// breaking, and cost accounting. A version that cannot serve these fails
// conformance validation at install time, not on the first production call.
type Conformance struct {
	// Schema is the normalized entity/field shape the provider returns (JSON Schema).
	Schema string `json:"schema"`
	// IdempotencyHeader, when set, is the header the provider honors for
	// deduplicating retried fetches (effect.ProviderIdempotent). Empty = at-least-once.
	IdempotencyHeader string `json:"idempotency_header,omitempty"`
	// SupportsPagination reports the provider can page large result sets.
	SupportsPagination bool `json:"supports_pagination"`
	// MaxRetries bounds automatic retries (0 = no retry).
	MaxRetries int `json:"max_retries"`
	// TimeoutSeconds bounds one fetch (required, > 0).
	TimeoutSeconds int `json:"timeout_seconds"`
	// CircuitBreakerFailureThreshold trips the circuit after this many consecutive
	// failures (0 = no circuit breaker).
	CircuitBreakerFailureThreshold int `json:"circuit_breaker_failure_threshold,omitempty"`
	// CostPerFetchUSD is the per-invocation cost, for quota/visibility.
	CostPerFetchUSD float64 `json:"cost_per_fetch_usd,omitempty"`
}

// Validate checks the conformance contract is satisfiable.
func (c Conformance) Validate() error {
	if strings.TrimSpace(c.Schema) == "" {
		return errors.New("providers: conformance schema is required")
	}
	if c.TimeoutSeconds <= 0 {
		return errors.New("providers: conformance timeout_seconds must be positive")
	}
	if c.MaxRetries < 0 {
		return errors.New("providers: conformance max_retries must be non-negative")
	}
	if c.CostPerFetchUSD < 0 {
		return errors.New("providers: conformance cost_per_fetch_usd must be non-negative")
	}
	return nil
}

// Manifest is one immutable provider version: identity, the connector type that
// backs it, capabilities, and the conformance contract.
type Manifest struct {
	Name        string      `json:"name"`
	Version     int         `json:"version"`
	Connector   string      `json:"connector"`
	Description string      `json:"description"`
	Conformance Conformance `json:"conformance"`
	DefinedBy   string      `json:"defined_by"`
	DefinedAt   time.Time   `json:"defined_at"`
}

// Validate checks the manifest is complete and well-formed.
func (m Manifest) Validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("providers: manifest name is required")
	}
	if !isValidKey(m.Name) {
		return fmt.Errorf("providers: manifest name %q must be lowercase alphanumeric with dashes", m.Name)
	}
	if m.Version < 1 {
		return fmt.Errorf("providers: manifest version %d must be positive", m.Version)
	}
	if strings.TrimSpace(m.Connector) == "" {
		return errors.New("providers: manifest connector is required")
	}
	if strings.TrimSpace(m.Description) == "" {
		return errors.New("providers: manifest description is required")
	}
	return m.Conformance.Validate()
}

// Configuration is the per-environment setup for a provider version: the
// connector's resolved config (with credentials, sealed at rest) and any
// provider-specific overrides.
type Configuration struct {
	Environment  Environment    `json:"environment"`
	Config       map[string]any `json:"config"`
	ConfiguredBy string         `json:"configured_by"`
	ConfiguredAt time.Time      `json:"configured_at"`
}

// TestEvidence is the recorded result of validating a provider version against
// its sandbox fixture before it may be approved.
type TestEvidence struct {
	Passed    bool      `json:"passed"`
	Fixture   string    `json:"fixture"`
	LatencyMs int64     `json:"latency_ms"`
	Details   string    `json:"details"`
	TestedBy  string    `json:"tested_by"`
	TestedAt  time.Time `json:"tested_at"`
}

// Approval is the four-eyes gate between tested and deployed: an independent
// actor (not the installer or the version's author) confirms the provider may
// serve an environment.
type Approval struct {
	RequestID  string    `json:"request_id"`
	ApprovedBy string    `json:"approved_by"`
	Reason     string    `json:"reason"`
	ApprovedAt time.Time `json:"approved_at"`
}

// isValidKey mirrors tenancy's slug rule: lowercase alphanumerics and dashes.
func isValidKey(key string) bool {
	if len(key) < 2 || len(key) > 64 {
		return false
	}
	for _, r := range key {
		if r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
