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
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/agent-manager/domain"
	"github.com/e6qu/intraktible/agent-manager/events"
	caseevents "github.com/e6qu/intraktible/case-manager/events"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// asyncJob is a queued agent run picked up by a worker.
type asyncJob struct {
	id     identity.Identity
	runID  string
	agent  string
	prompt string
}

// Handler records agent definitions and runs.
type Handler struct {
	log   eventlog.Log
	store store.Store
	reg   *ai.Registry
	tools agents.Toolbox
	now   func() time.Time
	newID func() string

	jobs chan asyncJob // async run queue (drained by workers)
	wg   sync.WaitGroup
}

// asyncQueueSize bounds the in-flight async run queue; a full queue makes
// StartRun fall back to running the agent synchronously rather than dropping it.
const asyncQueueSize = 256

// Option configures a Handler.
type Option func(*Handler)

// WithToolbox supplies the toolbox used to execute an agent's declared tools.
func WithToolbox(tb agents.Toolbox) Option {
	return func(h *Handler) { h.tools = tb }
}

// NewHandler builds a Handler over the log, the read store (to resolve agent
// definitions at run time), and the AI provider registry.
func NewHandler(log eventlog.Log, st store.Store, reg *ai.Registry, opts ...Option) *Handler {
	h := &Handler{
		log: log, store: st, reg: reg,
		now:   func() time.Time { return time.Now().UTC() },
		newID: newID,
		jobs:  make(chan asyncJob, asyncQueueSize),
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// RunResult is the outcome of a run returned to the caller.
type RunResult struct {
	RunID      string
	Status     domain.RunStatus
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
	out, err := agents.InvokeWithTools(ctx, h.store, h.reg, h.tools, id, agent, prompt)
	if err != nil {
		return RunResult{}, err
	}
	runID := h.newID()
	if _, err := h.append(ctx, id, events.TypeAgentRunRecorded, events.AgentRunRecorded{
		RunID: runID, Agent: agent, Model: out.Model, Prompt: prompt,
		Status: string(out.Status), Text: out.Text, Structured: out.Structured, ToolCalls: out.ToolCalls, Error: out.Error,
		PromptTokens: out.Usage.PromptTokens, CompletionTokens: out.Usage.CompletionTokens, At: h.now(),
	}); err != nil {
		return RunResult{}, err
	}
	return RunResult{RunID: runID, Status: out.Status, Text: out.Text, Structured: out.Structured, Error: out.Error}, nil
}

// StreamRun runs the named agent, streaming text deltas to onChunk as they
// arrive, and records the terminal run (the full text) like RunAgent — so a
// streamed run is just as auditable and replay reads the recorded output.
func (h *Handler) StreamRun(ctx context.Context, id identity.Identity, agent, prompt string, onChunk ai.StreamHandler) (RunResult, error) {
	if err := id.Valid(); err != nil {
		return RunResult{}, err
	}
	out, err := agents.InvokeStream(ctx, h.store, h.reg, h.tools, id, agent, prompt, onChunk)
	if err != nil {
		return RunResult{}, err
	}
	runID := h.newID()
	if _, err := h.append(ctx, id, events.TypeAgentRunRecorded, events.AgentRunRecorded{
		RunID: runID, Agent: agent, Model: out.Model, Prompt: prompt,
		Status: string(out.Status), Text: out.Text, Structured: out.Structured, ToolCalls: out.ToolCalls, Error: out.Error,
		PromptTokens: out.Usage.PromptTokens, CompletionTokens: out.Usage.CompletionTokens, At: h.now(),
	}); err != nil {
		return RunResult{}, err
	}
	return RunResult{RunID: runID, Status: out.Status, Text: out.Text, Structured: out.Structured, Error: out.Error}, nil
}

// StartRun accepts an agent run for asynchronous execution: it records an
// AgentRunStarted (status "running") and queues the work, returning the run id
// immediately. A worker later invokes the provider and records the terminal
// AgentRunRecorded; callers poll GET /v1/agent-runs/{run_id} for the outcome. An
// unknown agent is rejected up front (the read model is the same one RunAgent uses).
func (h *Handler) StartRun(ctx context.Context, id identity.Identity, agent, prompt string) (string, error) {
	if err := id.Valid(); err != nil {
		return "", err
	}
	if _, ok, err := agents.Read(ctx, h.store, id, agent); err != nil {
		return "", err
	} else if !ok {
		return "", fmt.Errorf("agent-manager: unknown agent %q", agent)
	}
	runID := h.newID()
	if _, err := h.append(ctx, id, events.TypeAgentRunStarted, events.AgentRunStarted{
		RunID: runID, Agent: agent, Prompt: prompt, At: h.now(),
	}); err != nil {
		return "", err
	}
	job := asyncJob{id: id, runID: runID, agent: agent, prompt: prompt}
	select {
	case h.jobs <- job:
		// queued for a worker
	default:
		// Queue full: run it rather than drop it (never lost), but in a tracked
		// goroutine — not inline — so StartRun still returns immediately and keeps
		// the 202 async contract. A background context (like the worker path) means
		// a client disconnect mid-call doesn't abort the run into a spurious failure;
		// h.wg lets DrainWorkers wait it out on shutdown.
		h.wg.Add(1)
		// #nosec G118 -- intentional: background ctx so a client disconnect doesn't
		// abort the run; DrainWorkers bounds the wait via h.wg.
		go func() {
			defer h.wg.Done()
			h.process(context.Background(), job)
		}()
	}
	return runID, nil
}

// StartWorkers launches n goroutines that drain the async run queue until ctx is
// cancelled; an in-flight run finishes (so shutdown never corrupts it into a
// failure) while a queued-but-unstarted run stays "running" and is recovered on
// the next boot. Pair with DrainWorkers to wait for them before closing the log.
func (h *Handler) StartWorkers(ctx context.Context, n int) {
	for i := 0; i < n; i++ {
		h.wg.Add(1)
		// The worker deliberately processes an in-flight run on a background context
		// (not ctx) so a graceful shutdown lets it finish rather than aborting it
		// into a spurious failure; DrainWorkers bounds the wait. The provider's own
		// timeout caps a hung call.
		go h.worker(ctx) // #nosec G118 -- intentional: in-flight runs survive shutdown
	}
}

// DrainWorkers blocks until all workers have stopped (after ctx cancellation).
func (h *Handler) DrainWorkers() { h.wg.Wait() }

func (h *Handler) worker(ctx context.Context) {
	defer h.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-h.jobs:
			// Process with a background context so a shutdown signal does not abort
			// (and thus fail) a run already in flight.
			h.process(context.Background(), job)
		}
	}
}

// process invokes the agent and records the terminal run event.
func (h *Handler) process(ctx context.Context, job asyncJob) {
	out, err := agents.InvokeWithTools(ctx, h.store, h.reg, h.tools, job.id, job.agent, job.prompt)
	if err != nil {
		// An unexpected resolution error (e.g. the agent was deleted mid-flight):
		// record it as a failed run so the run does not hang "running" forever.
		out = agents.Outcome{Status: domain.RunFailed, Error: err.Error()}
	}
	if _, aerr := h.append(ctx, job.id, events.TypeAgentRunRecorded, events.AgentRunRecorded{
		RunID: job.runID, Agent: job.agent, Model: out.Model, Prompt: job.prompt,
		Status: string(out.Status), Text: out.Text, Structured: out.Structured, ToolCalls: out.ToolCalls, Error: out.Error,
		PromptTokens: out.Usage.PromptTokens, CompletionTokens: out.Usage.CompletionTokens, At: h.now(),
	}); aerr != nil {
		slog.Error("agent-manager: failed to record async run", "run_id", job.runID, "err", aerr)
	}
}

// RecoverRunning re-enqueues runs left "running" by a crash or shutdown — those
// with an AgentRunStarted but no matching AgentRunRecorded — across all tenants.
// It folds the log (the source of truth), so it is safe to call at boot before
// the projections are rebuilt. Returns the number re-enqueued.
func (h *Handler) RecoverRunning(ctx context.Context) (int, error) {
	evs, err := h.log.Read(ctx, 0)
	if err != nil {
		return 0, fmt.Errorf("agent-manager: read log: %w", err)
	}
	type pending struct {
		id     identity.Identity
		agent  string
		prompt string
	}
	started := map[string]pending{}
	for _, e := range evs {
		if e.Stream != events.StreamAgents {
			continue
		}
		switch e.Type {
		case events.TypeAgentRunStarted:
			var p events.AgentRunStarted
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return 0, fmt.Errorf("agent-manager: decode run_started seq %d: %w", e.Seq, err)
			}
			started[p.RunID] = pending{
				id:    identity.Identity{Org: e.Org, Workspace: e.Workspace, Actor: e.Actor},
				agent: p.Agent, prompt: p.Prompt,
			}
		case events.TypeAgentRunRecorded:
			var p events.AgentRunRecorded
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return 0, fmt.Errorf("agent-manager: decode run_recorded seq %d: %w", e.Seq, err)
			}
			delete(started, p.RunID) // terminal reached — not pending
		}
	}
	enqueued := 0
	for runID, p := range started {
		job := asyncJob{id: p.id, runID: runID, agent: p.agent, prompt: p.prompt}
		select {
		case h.jobs <- job:
			enqueued++
		case <-ctx.Done():
			// Shutdown during recovery: stop rather than block forever on a full
			// queue whose workers have already exited. The not-yet-enqueued runs
			// stay "running" and are recovered on the next boot.
			return enqueued, ctx.Err()
		}
	}
	return enqueued, nil
}

// EscalateRun opens a human-review case from an existing agent run. It emits the
// Case Manager's own ReviewRequested event (which the cases projector already
// consumes), linking the case back to the run via its context — the build-order
// direction is one-way (this later module imports case-manager, never the reverse).
func (h *Handler) EscalateRun(ctx context.Context, id identity.Identity, cmd domain.EscalateRun) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	agent, ok, err := h.runAgentName(ctx, id, cmd.RunID)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	if !ok {
		return "", eventlog.Envelope{}, fmt.Errorf("agent-manager: unknown run %q", cmd.RunID)
	}
	caseID := h.newID()
	source, err := json.Marshal(map[string]string{"source": "agent", "agent": agent, "run_id": cmd.RunID})
	if err != nil {
		return "", eventlog.Envelope{}, fmt.Errorf("agent-manager: marshal escalation context: %w", err)
	}
	e, err := eventlog.AppendJSON(ctx, h.log, id.Org, id.Workspace, id.Actor,
		caseevents.StreamCases, caseevents.TypeReviewRequested, h.now(), caseevents.ReviewRequested{
			CaseID: caseID, CompanyName: cmd.CompanyName, CaseType: cmd.CaseType, SLADays: cmd.SLADays, Context: source,
		})
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	return caseID, e, nil
}

// runAgentName resolves a run for the tenant and returns its agent. It reads the
// tenant-scoped projection first — an O(1) keyed lookup that avoids folding the
// whole log (every tenant's events) on each escalate. The projection is eventually
// consistent, so on a miss (e.g. a run escalated in the same breath it was recorded,
// before the projection caught up) it falls back to a tenant-scoped fold of the log,
// which is immediately consistent.
func (h *Handler) runAgentName(ctx context.Context, id identity.Identity, runID string) (string, bool, error) {
	if run, ok, err := agents.GetRun(ctx, h.store, id, runID); err != nil {
		return "", false, fmt.Errorf("agent-manager: read run: %w", err)
	} else if ok {
		return run.Agent, true, nil
	}
	return h.runAgentNameFromLog(ctx, id, runID)
}

// runAgentNameFromLog folds the log (tenant-scoped) as the immediately-consistent
// fallback when the projection hasn't yet observed a just-recorded run.
func (h *Handler) runAgentNameFromLog(ctx context.Context, id identity.Identity, runID string) (string, bool, error) {
	evs, err := h.log.Read(ctx, 0)
	if err != nil {
		return "", false, fmt.Errorf("agent-manager: read log: %w", err)
	}
	for _, e := range evs {
		if e.Stream != events.StreamAgents || e.Type != events.TypeAgentRunRecorded ||
			e.Org != id.Org || e.Workspace != id.Workspace {
			continue
		}
		var p events.AgentRunRecorded
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return "", false, fmt.Errorf("agent-manager: decode run seq %d: %w", e.Seq, err)
		}
		if p.RunID == runID {
			return p.Agent, true, nil
		}
	}
	return "", false, nil
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ string, payload any) (eventlog.Envelope, error) {
	return eventlog.AppendJSON(ctx, h.log, id.Org, id.Workspace, id.Actor, events.StreamAgents, typ, h.now(), payload)
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
