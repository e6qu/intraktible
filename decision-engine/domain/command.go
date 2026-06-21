// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// slugPattern constrains a flow slug to a URL-safe form: it appears in the
// decide path, so it must be lowercase letters, digits, and hyphens.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Environment is a decide environment. It is a named type (not a bare string) so
// a command carries a validated value rather than an arbitrary string that could
// miss a deployment-map lookup deep in the decide path.
type Environment string

// Decide environments, in promotion order (sandbox → staging → production).
const (
	EnvSandbox    Environment = "sandbox"
	EnvStaging    Environment = "staging"
	EnvProduction Environment = "production"
)

var environments = map[Environment]bool{EnvSandbox: true, EnvStaging: true, EnvProduction: true}

// PromotionOrder lists environments from least to most production-like.
var PromotionOrder = []Environment{EnvSandbox, EnvStaging, EnvProduction}

// ValidEnvironment reports whether a raw string names a known environment — the
// boundary helper for request/path strings before they are typed. (Callers hold raw
// strings at the wire boundary, so this free function — not a method — is the form
// in use.)
func ValidEnvironment(env string) bool { return environments[Environment(env)] }

// Variant names which deployed version served a decision: the steady-state champion
// or the A/B challenger. A named type (not bare "champion"/"challenger" literals
// scattered across the decide path and analytics) so the values have one source.
type Variant string

const (
	VariantChampion   Variant = "champion"
	VariantChallenger Variant = "challenger"
)

// Valid reports whether v is a known variant.
func (v Variant) Valid() bool { return v == VariantChampion || v == VariantChallenger }

// CreateFlow is the command to register a new flow.
type CreateFlow struct {
	Slug string
	Name string
}

// Validate fails loudly on a malformed slug or empty name.
func (c CreateFlow) Validate() error {
	if !slugPattern.MatchString(c.Slug) {
		return fmt.Errorf("decision-engine: invalid slug %q (lowercase letters, digits, hyphens)", c.Slug)
	}
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("decision-engine: flow name is required")
	}
	return nil
}

// SetShadow assigns the shadow version for an environment (Version 0 clears it).
// The shadow version is evaluated alongside live decisions for divergence
// analysis; its result is never returned.
type SetShadow struct {
	FlowID      string
	Environment string
	Version     int
}

// Validate requires a flow, a known environment, and a non-negative version.
func (c SetShadow) Validate() error {
	if strings.TrimSpace(c.FlowID) == "" {
		return errors.New("decision-engine: flow_id is required")
	}
	if !ValidEnvironment(c.Environment) {
		return fmt.Errorf("decision-engine: invalid environment %q", c.Environment)
	}
	if c.Version < 0 {
		return fmt.Errorf("decision-engine: shadow version must be >= 0, got %d", c.Version)
	}
	return nil
}

// ImportFlow upserts a flow from an exported document (create-if-new, then
// publish the graph as a new version), so flows can be managed as code.
type ImportFlow struct {
	Slug        string
	Name        string
	Graph       events.Graph
	InputSchema json.RawMessage
}

// Validate requires a well-formed slug and a structurally valid graph. Name is
// optional on import — it defaults to the slug.
func (c ImportFlow) Validate() error {
	if !slugPattern.MatchString(c.Slug) {
		return fmt.Errorf("decision-engine: invalid slug %q (lowercase letters, digits, hyphens)", c.Slug)
	}
	return ValidateFlow(c.Graph)
}

// PublishVersion is the command to publish a new immutable version of a flow.
type PublishVersion struct {
	FlowID      string
	Graph       events.Graph
	InputSchema json.RawMessage
}

// Validate requires a target flow and a structurally valid graph.
func (c PublishVersion) Validate() error {
	if strings.TrimSpace(c.FlowID) == "" {
		return errors.New("decision-engine: flow_id is required")
	}
	return ValidateFlow(c.Graph)
}

// DeployVersion is the command to make a flow version live in an environment,
// optionally with an A/B challenger receiving ChallengerPct percent of traffic.
type DeployVersion struct {
	FlowID            string
	Environment       string
	Version           int
	ChallengerVersion int
	ChallengerPct     int
}

// Validate checks the environment and the version/challenger bounds. Whether the
// referenced versions exist is checked by the handler against the log.
func (c DeployVersion) Validate() error {
	if strings.TrimSpace(c.FlowID) == "" {
		return errors.New("decision-engine: flow_id is required")
	}
	if !ValidEnvironment(c.Environment) {
		return fmt.Errorf("decision-engine: invalid environment %q (sandbox|staging|production)", c.Environment)
	}
	if c.Version < 1 {
		return fmt.Errorf("decision-engine: version must be >= 1, got %d", c.Version)
	}
	if c.ChallengerVersion < 0 {
		return fmt.Errorf("decision-engine: challenger_version must be >= 0, got %d", c.ChallengerVersion)
	}
	if c.ChallengerPct < 0 || c.ChallengerPct > 100 {
		return fmt.Errorf("decision-engine: challenger_pct must be 0..100, got %d", c.ChallengerPct)
	}
	if c.ChallengerPct > 0 && c.ChallengerVersion < 1 {
		return errors.New("decision-engine: challenger_pct set without a challenger_version")
	}
	return nil
}

// SetPromotionPolicy configures promotion gates per target environment.
type SetPromotionPolicy struct {
	FlowID string
	Policy map[string]events.PromotionStagePolicy
}

// Validate checks that each configured stage is known and cannot disable the
// mandatory production maker-checker gate.
func (c SetPromotionPolicy) Validate() error {
	if strings.TrimSpace(c.FlowID) == "" {
		return errors.New("decision-engine: flow_id is required")
	}
	if len(c.Policy) == 0 {
		return errors.New("decision-engine: promotion policy is required")
	}
	for env, stage := range c.Policy {
		if !ValidEnvironment(env) {
			return fmt.Errorf("decision-engine: invalid promotion policy environment %q", env)
		}
		if Environment(env) == EnvProduction && !stage.RequireReview {
			return errors.New("decision-engine: production promotions require review")
		}
	}
	return nil
}
