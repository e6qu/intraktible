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
	TypeAgentDefined            = "agents.defined"
	TypeAgentRunStarted         = "agents.run_started"
	TypeAgentRunClaimed         = "agents.run_claimed"
	TypeAgentRunHeartbeat       = "agents.run_heartbeat"
	TypeAgentRunRetryRequested  = "agents.run_retry_requested"
	TypeAgentRunCancelRequested = "agents.run_cancel_requested"
	TypeAgentRunDeadLettered    = "agents.run_dead_lettered"
	TypeAgentRunRecorded        = "agents.run_recorded"
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

// AgentRunStarted records that an async run has been accepted and queued (status
// "running"), before the provider is called. It carries the prompt so a run left
// running by a crash/shutdown can be recovered (re-enqueued) on boot. A
// subsequent AgentRunRecorded for the same RunID folds it to its terminal state.
type AgentRunStarted struct {
	RunID              string    `json:"run_id"`
	Agent              string    `json:"agent"`
	Prompt             string    `json:"prompt"`
	Version            int       `json:"version,omitempty"`
	TimeoutMS          int64     `json:"timeout_ms,omitempty"`
	MaxAttempts        int       `json:"max_attempts"`
	IdempotencyKeyHash string    `json:"idempotency_key_hash,omitempty"`
	RequestHash        string    `json:"request_hash,omitempty"`
	BusinessReference  string    `json:"business_reference,omitempty"`
	CorrelationID      string    `json:"correlation_id,omitempty"`
	At                 time.Time `json:"at"`
}

type AgentRunClaimed struct {
	RunID      string    `json:"run_id"`
	Owner      string    `json:"owner"`
	Attempt    int       `json:"attempt"`
	LeaseUntil time.Time `json:"lease_until"`
}

type AgentRunHeartbeat struct {
	RunID      string    `json:"run_id"`
	Owner      string    `json:"owner"`
	Attempt    int       `json:"attempt"`
	LeaseUntil time.Time `json:"lease_until"`
}

type AgentRunRetryRequested struct {
	RunID                  string    `json:"run_id"`
	Actor                  string    `json:"actor"`
	AcknowledgeAtLeastOnce bool      `json:"acknowledge_at_least_once"`
	Reason                 string    `json:"reason"`
	At                     time.Time `json:"at"`
}

type AgentRunCancelRequested struct {
	RunID string    `json:"run_id"`
	Actor string    `json:"actor"`
	At    time.Time `json:"at"`
}

type AgentRunDeadLettered struct {
	RunID   string    `json:"run_id"`
	Agent   string    `json:"agent"`
	Prompt  string    `json:"prompt"`
	Attempt int       `json:"attempt"`
	Error   string    `json:"error"`
	At      time.Time `json:"at"`
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
	// PromptTokens/CompletionTokens are the provider-reported token consumption for
	// the run (0 when the provider does not report it), the durable basis for cost
	// attribution. Added later; both omitempty so already-recorded runs decode
	// unchanged (they report 0) — the event shape stays replay-stable.
	PromptTokens     int       `json:"prompt_tokens,omitempty"`
	CompletionTokens int       `json:"completion_tokens,omitempty"`
	Attempt          int       `json:"attempt,omitempty"`
	At               time.Time `json:"at"`
}

// ToolCall is one recorded tool invocation made during a run.
type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
}
