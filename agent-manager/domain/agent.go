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
