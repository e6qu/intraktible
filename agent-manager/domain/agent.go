// SPDX-License-Identifier: AGPL-3.0-or-later

// Package domain is the Agent Manager's functional core: pure types and command
// validation, no I/O.
package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Agent run statuses.
const (
	RunRunning   = "running" // an async run that has started but not yet finished
	RunCompleted = "completed"
	RunFailed    = "failed"
)

// DefineAgent registers (or redefines) an agent: a configuration over the
// pluggable AI provider — a system prompt, an optional model + provider
// selection, an optional structured-output JSON Schema, and a declared tool set.
type DefineAgent struct {
	Name     string
	Provider string // AI provider name; empty = the registry default
	Model    string
	System   string
	Schema   json.RawMessage // optional JSON-Schema for structured output
	Tools    []string
}

// EscalateRun opens a human-review case from an agent run (human-in-the-loop).
type EscalateRun struct {
	RunID       string
	CompanyName string
	CaseType    string
	SLADays     int
}

// Validate requires the run + the case fields the Case Manager needs.
func (c EscalateRun) Validate() error {
	if strings.TrimSpace(c.RunID) == "" {
		return errors.New("agent-manager: run_id is required")
	}
	if strings.TrimSpace(c.CompanyName) == "" {
		return errors.New("agent-manager: company_name is required")
	}
	if strings.TrimSpace(c.CaseType) == "" {
		return errors.New("agent-manager: case_type is required")
	}
	if c.SLADays < 0 {
		return fmt.Errorf("agent-manager: sla_days must be >= 0, got %d", c.SLADays)
	}
	return nil
}

// Validate requires a name and, if present, a JSON-object schema and non-blank
// tool names.
func (c DefineAgent) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("agent-manager: agent name is required")
	}
	if len(c.Schema) > 0 {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(c.Schema, &obj); err != nil {
			return fmt.Errorf("agent-manager: schema must be a JSON object: %w", err)
		}
	}
	for i, t := range c.Tools {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("agent-manager: tool %d is blank", i)
		}
	}
	return nil
}
