// SPDX-License-Identifier: AGPL-3.0-or-later

// Package agents is the Agent Manager's read model: a projector that folds agent
// definitions and run records into documents (the agent registry plus a run log
// for monitoring), and the read-time helper that invokes an agent's AI provider.
package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/agent-manager/events"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collections held by this read model.
const (
	CollectionAgents = "agents"
	CollectionRuns   = "agent_runs"
)

// AgentView is the materialized read model for one agent definition.
type AgentView struct {
	Org       string          `json:"org"`
	Workspace string          `json:"workspace"`
	Name      string          `json:"name"`
	Provider  string          `json:"provider,omitempty"`
	Model     string          `json:"model,omitempty"`
	System    string          `json:"system,omitempty"`
	Schema    json.RawMessage `json:"schema,omitempty"`
	Tools     []string        `json:"tools,omitempty"`
	Runs      int             `json:"runs"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// RunView is one recorded agent invocation.
type RunView struct {
	Org        string          `json:"org"`
	Workspace  string          `json:"workspace"`
	RunID      string          `json:"run_id"`
	Agent      string          `json:"agent"`
	Model      string          `json:"model,omitempty"`
	Prompt     string          `json:"prompt"`
	Status     string          `json:"status"`
	Text       string          `json:"text,omitempty"`
	Structured json.RawMessage `json:"structured,omitempty"`
	Error      string          `json:"error,omitempty"`
	Seq        uint64          `json:"seq"`
	At         time.Time       `json:"at"`
}

// Projector folds agent events into the registry + run-log read models.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "agents" }

// Apply maintains the agent registry and the run log.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeAgentDefined:
		return applyDefined(ctx, e, s)
	case events.TypeAgentRunRecorded:
		return applyRun(ctx, e, s)
	default:
		return nil
	}
}

func applyDefined(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.AgentDefined
	if err := decode(e, &p); err != nil {
		return err
	}
	key := store.Key(e.Org, e.Workspace, p.Name)
	cur, _, err := store.GetDoc[AgentView](ctx, s, CollectionAgents, key)
	if err != nil {
		return err
	}
	v := AgentView{
		Org: e.Org, Workspace: e.Workspace, Name: p.Name,
		Provider: p.Provider, Model: p.Model, System: p.System, Schema: p.Schema, Tools: p.Tools,
		Runs: cur.Runs, UpdatedAt: e.Time,
	}
	return store.PutDoc(ctx, s, CollectionAgents, key, v)
}

func applyRun(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.AgentRunRecorded
	if err := decode(e, &p); err != nil {
		return err
	}
	run := RunView{
		Org: e.Org, Workspace: e.Workspace,
		RunID: p.RunID, Agent: p.Agent, Model: p.Model, Prompt: p.Prompt,
		Status: p.Status, Text: p.Text, Structured: p.Structured, Error: p.Error,
		Seq: e.Seq, At: p.At,
	}
	if err := store.PutDoc(ctx, s, CollectionRuns, store.Key(e.Org, e.Workspace, p.RunID), run); err != nil {
		return err
	}
	// Bump the agent's run counter (the agent may not exist if it was deleted; a
	// run for an unknown agent still lands in the run log).
	key := store.Key(e.Org, e.Workspace, p.Agent)
	c, ok, err := store.GetDoc[AgentView](ctx, s, CollectionAgents, key)
	if err != nil || !ok {
		return err
	}
	c.Runs++
	return store.PutDoc(ctx, s, CollectionAgents, key, c)
}

// Read returns one agent definition for id's tenant.
func Read(ctx context.Context, s store.Store, id identity.Identity, name string) (AgentView, bool, error) {
	return store.GetDoc[AgentView](ctx, s, CollectionAgents, store.Key(id.Org, id.Workspace, name))
}

// List returns the tenant's agent definitions, ordered by name.
func List(ctx context.Context, s store.Store, id identity.Identity) ([]AgentView, error) {
	return store.QueryDocs(ctx, s, CollectionAgents, store.Key(id.Org, id.Workspace, ""),
		nil, func(a, b AgentView) bool { return a.Name < b.Name })
}

// GetRun returns one run for id's tenant.
func GetRun(ctx context.Context, s store.Store, id identity.Identity, runID string) (RunView, bool, error) {
	return store.GetDoc[RunView](ctx, s, CollectionRuns, store.Key(id.Org, id.Workspace, runID))
}

// ListRuns returns the tenant's runs, optionally filtered by agent, newest first.
func ListRuns(ctx context.Context, s store.Store, id identity.Identity, agent string) ([]RunView, error) {
	return store.QueryDocs(ctx, s, CollectionRuns, store.Key(id.Org, id.Workspace, ""),
		func(r RunView) bool { return agent == "" || r.Agent == agent },
		func(a, b RunView) bool { return a.Seq > b.Seq })
}

// Outcome is the result of invoking an agent: the resolved model, the run status,
// and the provider's text or structured output (or an error message on failure).
type Outcome struct {
	Model      string
	Status     string
	Text       string
	Structured json.RawMessage
	Error      string
}

// Invoke runs the named agent against prompt via its configured AI provider. A
// provider failure is captured as a failed Outcome (not an error); only an unknown
// agent or a misconfigured provider returns an error.
func Invoke(ctx context.Context, s store.Store, reg *ai.Registry, id identity.Identity, agent, prompt string) (Outcome, error) {
	def, ok, err := Read(ctx, s, id, agent)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{}, fmt.Errorf("agent-manager: unknown agent %q", agent)
	}
	p, err := reg.Get(def.Provider)
	if err != nil {
		return Outcome{}, err
	}
	resp, perr := p.Complete(ctx, ai.Request{Model: def.Model, System: def.System, Prompt: prompt, Schema: def.Schema})
	// A provider failure is converted into a recorded "failed" outcome, not a
	// call error — the caller records the run either way.
	out := Outcome{Model: def.Model, Status: domainRunCompleted}
	switch {
	case perr != nil:
		out.Status = domainRunFailed
		out.Error = perr.Error()
	default:
		out.Text = resp.Text
		out.Structured = resp.Structured
		if resp.Model != "" {
			out.Model = resp.Model
		}
	}
	return out, nil
}

// Provider adapts agent invocation to a prompt→JSON lookup, suitable as a
// decision-engine agent source (it satisfies that engine's AgentProvider port
// structurally, without this package importing it). It returns the agent's
// structured output when it has a schema, else {"text": …}; a failed run is an
// error so the calling decision fails loudly. The run is not recorded here — the
// decision records the output in its own event stream.
type Provider struct {
	Store    store.Store
	Registry *ai.Registry
}

// RunAgent runs the named agent against prompt and returns its output as JSON.
func (p Provider) RunAgent(ctx context.Context, id identity.Identity, agent, prompt string) (json.RawMessage, error) {
	out, err := Invoke(ctx, p.Store, p.Registry, id, agent, prompt)
	if err != nil {
		return nil, err
	}
	if out.Status != domainRunCompleted {
		return nil, fmt.Errorf("agent-manager: agent %q run failed: %s", agent, out.Error)
	}
	if len(out.Structured) > 0 {
		return out.Structured, nil
	}
	b, err := json.Marshal(map[string]string{"text": out.Text})
	if err != nil {
		return nil, fmt.Errorf("agent-manager: marshal agent text: %w", err)
	}
	return b, nil
}

// Mirror the domain run-status constants without importing the domain package
// (this read-model package stays free of the command/domain write side).
const (
	domainRunCompleted = "completed"
	domainRunFailed    = "failed"
)

func decode[T any](e eventlog.Envelope, v *T) error {
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return fmt.Errorf("agent-manager: decode %s seq %d: %w", e.Type, e.Seq, err)
	}
	return nil
}
