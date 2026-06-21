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

	"github.com/e6qu/intraktible/agent-manager/domain"
	"github.com/e6qu/intraktible/agent-manager/events"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/schema"
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
	Org        string            `json:"org"`
	Workspace  string            `json:"workspace"`
	RunID      string            `json:"run_id"`
	Agent      string            `json:"agent"`
	Model      string            `json:"model,omitempty"`
	Prompt     string            `json:"prompt"`
	Status     domain.RunStatus  `json:"status"`
	Text       string            `json:"text,omitempty"`
	Structured json.RawMessage   `json:"structured,omitempty"`
	ToolCalls  []events.ToolCall `json:"tool_calls,omitempty"`
	Error      string            `json:"error,omitempty"`
	Seq        uint64            `json:"seq"`
	At         time.Time         `json:"at"`
}

// Projector folds agent events into the registry + run-log read models.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "agents" }

// Collections lists the store collections this projector owns.
func (Projector) Collections() []string { return []string{CollectionAgents, CollectionRuns} }

// Apply maintains the agent registry and the run log.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeAgentDefined:
		return applyDefined(ctx, e, s)
	case events.TypeAgentRunStarted:
		return applyRunStarted(ctx, e, s)
	case events.TypeAgentRunRecorded:
		return applyRun(ctx, e, s)
	default:
		return nil
	}
}

// applyRunStarted materializes an in-flight (async) run as a "running" RunView. A
// later AgentRunRecorded for the same run id overwrites it with the outcome.
func applyRunStarted(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.AgentRunStarted
	if err := decode(e, &p); err != nil {
		return err
	}
	run := RunView{
		Org: e.Org, Workspace: e.Workspace,
		RunID: p.RunID, Agent: p.Agent, Prompt: p.Prompt,
		Status: domain.RunRunning, Seq: e.Seq, At: p.At,
	}
	return store.PutDoc(ctx, s, CollectionRuns, store.Key(e.Org, e.Workspace, p.RunID), run)
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
	runKey := store.Key(e.Org, e.Workspace, p.RunID)
	// Read the prior run state: the counter is bumped only on the FIRST terminal
	// recording for a RunID. A recovered/re-run run records a second
	// AgentRunRecorded for the same RunID (RecoverRunning); without this guard the
	// agent's run count would inflate on every recovery and not be replay-stable.
	prev, prevExists, err := store.GetDoc[RunView](ctx, s, CollectionRuns, runKey)
	if err != nil {
		return err
	}
	// Parse-guard the status at the decode boundary (like case-manager's projector):
	// an unknown value from a legacy/hand-crafted event must not land in the read
	// model where SummarizeRuns would miscount it. An unrecognized status records as
	// RunFailed — the safe terminal interpretation for an unparseable outcome.
	status, ok := domain.ParseRunStatus(p.Status)
	if !ok {
		status = domain.RunFailed
	}
	run := RunView{
		Org: e.Org, Workspace: e.Workspace,
		RunID: p.RunID, Agent: p.Agent, Model: p.Model, Prompt: p.Prompt,
		Status: status, Text: p.Text, Structured: p.Structured, ToolCalls: p.ToolCalls, Error: p.Error,
		Seq: e.Seq, At: p.At,
	}
	if err := store.PutDoc(ctx, s, CollectionRuns, runKey, run); err != nil {
		return err
	}
	// First terminal recording = the run was absent (a sync run) or still "running"
	// (the async started→recorded transition); a re-record of an already-terminal
	// run does not re-count.
	if prevExists && prev.Status != domain.RunRunning {
		return nil
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

// RunSummary is an at-a-glance roll-up of the run log for monitoring.
type RunSummary struct {
	Total     int            `json:"total"`
	Completed int            `json:"completed"`
	Failed    int            `json:"failed"`
	ByAgent   map[string]int `json:"by_agent"`
}

// SummarizeRuns rolls up a set of runs (counts by status and by agent).
func SummarizeRuns(runs []RunView) RunSummary {
	s := RunSummary{Total: len(runs), ByAgent: map[string]int{}}
	for _, r := range runs {
		s.ByAgent[r.Agent]++
		switch r.Status {
		case domain.RunCompleted:
			s.Completed++
		case domain.RunFailed:
			s.Failed++
		}
	}
	return s
}

// Outcome is the result of invoking an agent: the resolved model, the run status,
// and the provider's text or structured output (or an error message on failure).
// ToolCalls is the tool-calling trace when the agent used tools.
//
// Invariant (enforced by normalize before the value leaves this package): the
// payload agrees with the status — a RunFailed outcome carries only its Error (no
// Text/Structured), a RunCompleted outcome carries no Error. "completed with an
// error" / "failed with output" are therefore not observable states.
type Outcome struct {
	Model      string
	Status     domain.RunStatus
	Text       string
	Structured json.RawMessage
	ToolCalls  []events.ToolCall
	Error      string
}

// normalize enforces the Status⇄payload invariant. The tool-calling loop builds an
// Outcome up incrementally across steps, so rather than rely on every terminal
// branch to zero the inconsistent fields, this is applied once at each return so
// every recorded run is internally consistent.
func (o Outcome) normalize() Outcome {
	switch o.Status {
	case domain.RunFailed:
		o.Text, o.Structured = "", nil
	case domain.RunCompleted:
		o.Error = ""
	}
	return o
}

// Toolbox resolves an agent's declared tool names to provider tool specs and
// executes a tool call. It is supplied by the composition root (e.g. backed by
// Context Layer connectors); when nil, agents run without tools.
type Toolbox interface {
	// Spec returns the provider-facing description of a named tool.
	Spec(name string) (ai.Tool, bool)
	// Call executes the named tool with JSON arguments and returns its JSON result.
	Call(ctx context.Context, id identity.Identity, name string, args json.RawMessage) (json.RawMessage, error)
}

// maxToolSteps bounds the tool-calling loop so a model that keeps requesting
// tools (or a tool that always provokes another call) terminates loudly.
const maxToolSteps = 8

// Invoke runs the named agent against prompt via its configured AI provider,
// without tools. See InvokeWithTools.
func Invoke(ctx context.Context, s store.Store, reg *ai.Registry, id identity.Identity, agent, prompt string) (Outcome, error) {
	return InvokeWithTools(ctx, s, reg, nil, id, agent, prompt)
}

// InvokeWithTools runs the named agent. When the agent declares tools and a
// Toolbox is supplied, it drives a tool-calling loop: the provider may answer
// with tool calls, each is executed via the Toolbox and fed back, until the model
// returns a final answer or maxToolSteps is exceeded. A provider failure is
// captured as a failed Outcome (not an error); only an unknown agent or a
// misconfigured provider/tool returns an error... save where noted.
func InvokeWithTools(ctx context.Context, s store.Store, reg *ai.Registry, tb Toolbox, id identity.Identity, agent, prompt string) (Outcome, error) {
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
	tools, err := resolveTools(def.Tools, tb)
	if err != nil {
		// A declared tool with no Toolbox entry is a misconfiguration, not a run
		// outcome — fail loudly so it is fixed.
		return Outcome{}, err
	}
	// The declared tools ARE the capability boundary. The provider is sent only these
	// specs, but its response is untrusted — a buggy or prompt-injected model can name
	// any tool — so execution is gated on this set too (otherwise the model could drive
	// the toolbox to invoke any connector in the caller's tenant).
	allowed := make(map[string]bool, len(tools))
	for _, tl := range tools {
		allowed[tl.Name] = true
	}

	// The run state is mutated in place; every terminal branch sets the status and
	// error then falls through to a single `return out, nil` — a provider failure
	// is a recorded failed run, not a call error (so the linter's err-then-nil
	// guard does not apply).
	out := Outcome{Model: def.Model, Status: domain.RunCompleted}
	var history []ai.Message
	for step := 0; ; step++ {
		// Honor cancellation between steps: the loop fans out to a provider call and
		// arbitrary tool/connector calls each round, so a cancelled/expired context
		// (e.g. the client disconnected) must stop the work rather than run up to
		// maxToolSteps more billable rounds against a dead request.
		if err := ctx.Err(); err != nil {
			out.Status, out.Error = domain.RunFailed, err.Error()
			break
		}
		if step >= maxToolSteps {
			out.Status, out.Error = domain.RunFailed, fmt.Sprintf("agent-manager: tool-calling exceeded %d steps", maxToolSteps)
			break
		}
		resp, perr := p.Complete(ctx, ai.Request{
			Model: def.Model, System: def.System, Prompt: prompt, Schema: def.Schema, Tools: tools, History: history,
		})
		if resp.Model != "" {
			out.Model = resp.Model
		}
		switch {
		case perr != nil:
			out.Status, out.Error = domain.RunFailed, perr.Error()
		case len(resp.ToolCalls) == 0:
			out.Text, out.Structured = resp.Text, resp.Structured
			if verr := validateStructured(def.Schema, resp.Structured); verr != nil {
				out.Status, out.Error, out.Text, out.Structured = domain.RunFailed, verr.Error(), "", nil
			}
		case len(tools) == 0:
			// The model asked to call a tool the agent doesn't have. Record what it
			// tried before failing, so the run trace is a faithful record of the
			// provider interaction (not a silent failure).
			for _, c := range resp.ToolCalls {
				out.ToolCalls = append(out.ToolCalls, events.ToolCall{
					Name: c.Name, Arguments: c.Arguments, Error: "agent declares no tools",
				})
			}
			out.Status, out.Error = domain.RunFailed, "agent-manager: model requested a tool but the agent declares none"
		default:
			// The model wants tools: execute them, feed the results back, and loop.
			// Normalize the correlation IDs first — a model returning multiple calls
			// with empty or duplicate IDs would otherwise break the assistant↔result
			// pairing (strict providers 400 the next request; lenient ones misattribute
			// a result to the wrong call).
			calls := normalizeToolCallIDs(resp.ToolCalls)
			history = append(history, ai.Message{Role: "assistant", ToolCalls: calls})
			history = appendToolResults(ctx, tb, allowed, id, calls, history, &out)
			continue
		}
		break
	}
	return out.normalize(), nil
}

// InvokeStream runs the named agent, streaming text deltas to onChunk when the
// provider supports streaming and the agent declares no tools; otherwise it runs
// the normal (tool-calling) path and emits the final text as a single chunk, so
// callers get a uniform streaming interface. The Outcome is recorded by the caller.
func InvokeStream(ctx context.Context, s store.Store, reg *ai.Registry, tb Toolbox, id identity.Identity, agent, prompt string, onChunk ai.StreamHandler) (Outcome, error) {
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
	sp, streamable := p.(ai.StreamingProvider)
	if !streamable || len(def.Tools) > 0 {
		// Tools or a non-streaming provider: run fully, then emit the text once.
		out, ierr := InvokeWithTools(ctx, s, reg, tb, id, agent, prompt)
		if ierr != nil {
			return Outcome{}, ierr
		}
		if out.Text != "" && onChunk != nil {
			onChunk(ai.Chunk{Text: out.Text})
		}
		return out, nil
	}

	out := Outcome{Model: def.Model, Status: domain.RunCompleted}
	resp, perr := sp.Stream(ctx, ai.Request{Model: def.Model, System: def.System, Prompt: prompt, Schema: def.Schema}, onChunk)
	switch {
	case perr != nil:
		out.Status, out.Error = domain.RunFailed, perr.Error()
	default:
		if resp.Model != "" {
			out.Model = resp.Model
		}
		out.Text, out.Structured = resp.Text, resp.Structured
		if verr := validateStructured(def.Schema, resp.Structured); verr != nil {
			out.Status, out.Error, out.Text, out.Structured = domain.RunFailed, verr.Error(), "", nil
		}
	}
	return out.normalize(), nil
}

// resolveTools maps an agent's declared tool names to provider tool specs via the
// Toolbox, erroring if a declared tool is unknown. No declared tools (or no
// Toolbox) yields no tools — a plain completion.
func resolveTools(names []string, tb Toolbox) ([]ai.Tool, error) {
	if len(names) == 0 || tb == nil {
		return nil, nil
	}
	tools := make([]ai.Tool, 0, len(names))
	for _, name := range names {
		spec, ok := tb.Spec(name)
		if !ok {
			return nil, fmt.Errorf("agent-manager: agent declares unknown tool %q", name)
		}
		tools = append(tools, spec)
	}
	return tools, nil
}

// normalizeToolCallIDs returns the calls with every ID guaranteed non-empty and
// unique within the turn — synthesizing call_<i> for an empty ID and disambiguating a
// duplicate. The ID is the correlation key between the assistant's tool_calls and the
// tool-result messages fed back to the model; an empty or duplicated one breaks that
// pairing regardless of provider, so we never trust the model to get it right.
func normalizeToolCallIDs(calls []ai.ToolCall) []ai.ToolCall {
	seen := make(map[string]bool, len(calls))
	out := make([]ai.ToolCall, len(calls))
	for i, c := range calls {
		id := c.ID
		if id == "" || seen[id] {
			id = fmt.Sprintf("call_%d", i)
			for n := 0; seen[id]; n++ { // disambiguate against an already-used synthetic/real id
				id = fmt.Sprintf("call_%d_%d", i, n)
			}
		}
		seen[id] = true
		c.ID = id
		out[i] = c
	}
	return out
}

// appendToolResults executes each tool call the agent ALLOWED, records it in the
// outcome trace, and appends a tool-result message to the conversation. A tool error
// is fed back to the model (and recorded) rather than aborting — the model may
// recover. A call naming a tool not in `allowed` is refused without executing it (the
// declared-tool set is the capability boundary; the model's response is untrusted)
// and the refusal is recorded + fed back so the model can correct course.
func appendToolResults(ctx context.Context, tb Toolbox, allowed map[string]bool, id identity.Identity, calls []ai.ToolCall, history []ai.Message, out *Outcome) []ai.Message {
	for _, call := range calls {
		rec := events.ToolCall{Name: call.Name, Arguments: call.Arguments}
		if !allowed[call.Name] {
			rec.Error = "tool not declared by this agent"
			out.ToolCalls = append(out.ToolCalls, rec)
			history = append(history, ai.Message{Role: "tool", ToolCallID: call.ID, Content: rec.Error})
			continue
		}
		result, terr := tb.Call(ctx, id, call.Name, call.Arguments)
		content := ""
		if terr != nil {
			rec.Error = terr.Error()
			content = terr.Error()
		} else {
			rec.Result = result
			content = string(result)
		}
		out.ToolCalls = append(out.ToolCalls, rec)
		history = append(history, ai.Message{Role: "tool", ToolCallID: call.ID, Content: content})
	}
	return history
}

// validateStructured checks a schema-constrained response against the agent's
// schema. It is a no-op when the agent has no schema.
func validateStructured(agentSchema, structured json.RawMessage) error {
	if len(agentSchema) == 0 {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal(structured, &obj); err != nil {
		return fmt.Errorf("agent-manager: structured output is not a JSON object: %w", err)
	}
	return schema.ValidateObject(agentSchema, obj)
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
	Tools    Toolbox
}

// RunAgent runs the named agent against prompt and returns its output as JSON.
func (p Provider) RunAgent(ctx context.Context, id identity.Identity, agent, prompt string) (json.RawMessage, error) {
	out, err := InvokeWithTools(ctx, p.Store, p.Registry, p.Tools, id, agent, prompt)
	if err != nil {
		return nil, err
	}
	if out.Status != domain.RunCompleted {
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

func decode[T any](e eventlog.Envelope, v *T) error {
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return fmt.Errorf("agent-manager: decode %s seq %d: %w", e.Type, e.Seq, err)
	}
	return nil
}
