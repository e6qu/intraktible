// SPDX-License-Identifier: AGPL-3.0-or-later

// Package policy is the Decision Engine's operational layer over a flow: a
// declarative set of disposition rules (auto-approve / decline / refer) evaluated
// against a decision's output. It is a first-class, versioned, governed artifact —
// the shared brain for real-time, batch, and (later) pre-approval decisioning.
// The disposition logic here is pure and deterministic; the shell records which
// policy version applied, so a decision's disposition is replay-stable.
package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/expr-lang/expr"
)

// Disposition is the automated outcome a policy assigns to a decision. A named
// type (not a bare string) so an invalid disposition is caught at validation,
// not carried as an arbitrary string onto a recorded decision.
type Disposition string

// Disposition values.
const (
	Approve Disposition = "approve"
	Decline Disposition = "decline"
	Refer   Disposition = "refer" // the residual that needs a human
)

// Valid reports whether d is a known disposition.
func (d Disposition) Valid() bool {
	return d == Approve || d == Decline || d == Refer
}

// Rule maps a condition over the flow's output to a disposition. Rules are
// evaluated in order; the first whose When holds wins.
type Rule struct {
	When        string      `json:"when"`        // expr over the flow output (fields top-level, e.g. "score >= 0.85")
	Disposition Disposition `json:"disposition"` // approve | decline | refer
	Code        string      `json:"code,omitempty"`
	Description string      `json:"description,omitempty"`
}

// Spec is a policy body: ordered rules + the default disposition for the residual
// (decisions no rule matched). An empty Default means refer.
type Spec struct {
	Rules   []Rule      `json:"rules"`
	Default Disposition `json:"default,omitempty"`
}

// Outcome is the disposition a policy assigned, with the reason that drove it.
type Outcome struct {
	Disposition Disposition `json:"disposition"`
	Code        string      `json:"code,omitempty"`
	Description string      `json:"description,omitempty"`
}

// Validate checks the dispositions are known and every rule condition compiles.
func (s Spec) Validate() error {
	if s.Default != "" && !s.Default.Valid() {
		return fmt.Errorf("policy: invalid default disposition %q (approve|decline|refer)", s.Default)
	}
	for i, r := range s.Rules {
		if !r.Disposition.Valid() {
			return fmt.Errorf("policy: rule %d has invalid disposition %q (approve|decline|refer)", i, r.Disposition)
		}
		if r.When == "" {
			return fmt.Errorf("policy: rule %d has an empty condition", i)
		}
		if _, err := expr.Compile(r.When); err != nil {
			return fmt.Errorf("policy: rule %d condition %q: %w", i, r.When, err)
		}
	}
	return nil
}

// Apply evaluates the policy against a decision's output and returns the assigned
// disposition: the first matching rule's, otherwise the default (refer). A rule
// condition that errors (e.g. references a missing field) fails loudly.
func (s Spec) Apply(output map[string]any) (Outcome, error) {
	for i, r := range s.Rules {
		program, err := expr.Compile(r.When, expr.Env(output), expr.DisableBuiltin("now"))
		if err != nil {
			return Outcome{}, fmt.Errorf("policy: rule %d condition %q: %w", i, r.When, err)
		}
		v, err := expr.Run(program, output)
		if err != nil {
			return Outcome{}, fmt.Errorf("policy: rule %d condition %q: %w", i, r.When, err)
		}
		b, ok := v.(bool)
		if !ok {
			return Outcome{}, fmt.Errorf("policy: rule %d condition %q did not evaluate to a boolean", i, r.When)
		}
		if b {
			return Outcome{Disposition: r.Disposition, Code: r.Code, Description: r.Description}, nil
		}
	}
	d := s.Default
	if d == "" {
		d = Refer
	}
	return Outcome{Disposition: d}, nil
}

// Etag is the content hash of a policy spec, so an identical republish is detectable.
func Etag(s Spec) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("policy: hash spec: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
