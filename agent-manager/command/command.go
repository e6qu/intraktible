// SPDX-License-Identifier: AGPL-3.0-or-later

// Package command is the Agent Manager's write side (imperative shell): it
// validates via the functional core and appends events. Running an agent invokes
// the AI provider (an effect) and records the response, so the run is auditable
// and replay reads the recorded output rather than re-calling the model.
package command

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/agent-manager/domain"
	"github.com/e6qu/intraktible/agent-manager/events"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Handler records agent definitions and runs.
type Handler struct {
	log   eventlog.Log
	store store.Store
	reg   *ai.Registry
	now   func() time.Time
	newID func() string
}

// NewHandler builds a Handler over the log, the read store (to resolve agent
// definitions at run time), and the AI provider registry.
func NewHandler(log eventlog.Log, st store.Store, reg *ai.Registry) *Handler {
	return &Handler{
		log: log, store: st, reg: reg,
		now:   func() time.Time { return time.Now().UTC() },
		newID: newID,
	}
}

// RunResult is the outcome of a run returned to the caller.
type RunResult struct {
	RunID      string
	Status     string
	Text       string
	Structured json.RawMessage
	Error      string
}

// DefineAgent registers (or redefines) an agent.
func (h *Handler) DefineAgent(ctx context.Context, id identity.Identity, cmd domain.DefineAgent) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	return h.append(ctx, id, events.TypeAgentDefined, events.AgentDefined{
		Name: cmd.Name, Provider: cmd.Provider, Model: cmd.Model,
		System: cmd.System, Schema: cmd.Schema, Tools: cmd.Tools,
	})
}

// RunAgent invokes the named agent against prompt and records the run. A provider
// failure is a recorded "failed" run (returned with Status failed), not an API
// error; only an unknown agent / misconfigured provider returns an error.
func (h *Handler) RunAgent(ctx context.Context, id identity.Identity, agent, prompt string) (RunResult, error) {
	if err := id.Valid(); err != nil {
		return RunResult{}, err
	}
	out, err := agents.Invoke(ctx, h.store, h.reg, id, agent, prompt)
	if err != nil {
		return RunResult{}, err
	}
	runID := h.newID()
	if _, err := h.append(ctx, id, events.TypeAgentRunRecorded, events.AgentRunRecorded{
		RunID: runID, Agent: agent, Model: out.Model, Prompt: prompt,
		Status: out.Status, Text: out.Text, Structured: out.Structured, Error: out.Error, At: h.now(),
	}); err != nil {
		return RunResult{}, err
	}
	return RunResult{RunID: runID, Status: out.Status, Text: out.Text, Structured: out.Structured, Error: out.Error}, nil
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ string, payload any) (eventlog.Envelope, error) {
	return eventlog.AppendJSON(ctx, h.log, id.Org, id.Workspace, id.Actor, events.StreamAgents, typ, h.now(), payload)
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
