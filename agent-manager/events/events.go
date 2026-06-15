// SPDX-License-Identifier: AGPL-3.0-or-later

// Package events defines the Agent Manager's event payloads: an agent is
// registered via AgentDefined and each invocation is captured (with its provider
// response) by AgentRunRecorded — so a run is auditable and replay reads the
// recorded output rather than re-calling the (non-deterministic) model.
package events

import (
	"encoding/json"
	"time"
)

// StreamAgents is the Agent Manager's event stream.
const StreamAgents = "agents"

// Agent Manager event types.
const (
	TypeAgentDefined     = "agents.defined"
	TypeAgentRunRecorded = "agents.run_recorded"
)

// AgentDefined registers (or redefines) an agent's configuration.
type AgentDefined struct {
	Name     string          `json:"name"`
	Provider string          `json:"provider,omitempty"`
	Model    string          `json:"model,omitempty"`
	System   string          `json:"system,omitempty"`
	Schema   json.RawMessage `json:"schema,omitempty"`
	Tools    []string        `json:"tools,omitempty"`
}

// AgentRunRecorded records one agent invocation and its outcome. Text is set for a
// plain completion; Structured for a schema-constrained one; Error for a failure.
// ToolCalls records the tool-calling trace (the tools the model invoked and their
// results) so a run that used tools is fully auditable and replay-stable.
type AgentRunRecorded struct {
	RunID      string          `json:"run_id"`
	Agent      string          `json:"agent"`
	Model      string          `json:"model,omitempty"`
	Prompt     string          `json:"prompt"`
	Status     string          `json:"status"`
	Text       string          `json:"text,omitempty"`
	Structured json.RawMessage `json:"structured,omitempty"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	Error      string          `json:"error,omitempty"`
	At         time.Time       `json:"at"`
}

// ToolCall is one recorded tool invocation made during a run.
type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
}
