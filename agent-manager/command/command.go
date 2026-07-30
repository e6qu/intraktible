// SPDX-License-Identifier: AGPL-3.0-or-later

// Package command is the Agent Manager's write side (imperative shell): it
// validates via the functional core and appends events. Running an agent invokes
// the AI provider (an effect) and records the response, so the run is auditable
// and replay reads the recorded output rather than re-calling the model.
package command

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/agent-manager/domain"
	"github.com/e6qu/intraktible/agent-manager/eval"
	"github.com/e6qu/intraktible/agent-manager/events"
	caseevents "github.com/e6qu/intraktible/case-manager/events"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/effect"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// asyncJob is a queued agent run picked up by a worker.
type asyncJob struct {
	id      identity.Identity
	runID   string
	agent   string
	prompt  string
	version int
}

// Handler records agent definitions and runs.
type Handler struct {
	log   eventlog.Log
	store store.Store
	reg   *ai.Registry
	tools agents.Toolbox
	now   func() time.Time
	newID func() string

	jobs           chan asyncJob // local wake-up queue; the durable log is authoritative
	enqueueOnStart bool
	workerOwner    string
	activeMu       sync.Mutex
	active         map[string]context.CancelFunc
	wg             sync.WaitGroup
}

// asyncQueueSize bounds local worker wake-ups. The event log, not this channel,
// is the durable queue, so saturation only defers discovery to the next poll.
const asyncQueueSize = 256

const (
	defaultRunTimeout     = 60 * time.Second
	maxRunTimeout         = 10 * time.Minute
	defaultRunMaxAttempts = 3
	maxRunAttempts        = 10
	runLease              = 30 * time.Second
	runPollInterval       = 250 * time.Millisecond
)

// Option configures a Handler.
type Option func(*Handler)

// WithToolbox supplies the toolbox used to execute an agent's declared tools.
func WithToolbox(tb agents.Toolbox) Option {
	return func(h *Handler) { h.tools = tb }
}

// WithNow overrides the clock used to stamp recorded events (deterministic
// tests, the demo seeder).
func WithNow(now func() time.Time) Option {
	return func(h *Handler) { h.now = now }
}

// WithEnqueueOnStart controls whether the accepting process nudges its local
// worker queue. API-only replicas disable it; worker replicas discover the same
// durable AgentRunStarted event through their poller.
func WithEnqueueOnStart(enabled bool) Option {
	return func(h *Handler) { h.enqueueOnStart = enabled }
}

// NewHandler builds a Handler over the log, the read store (to resolve agent
// definitions at run time), and the AI provider registry.
func NewHandler(log eventlog.Log, st store.Store, reg *ai.Registry, opts ...Option) *Handler {
	h := &Handler{
		log: log, store: st, reg: reg,
		now:            func() time.Time { return time.Now().UTC() },
		newID:          newID,
		jobs:           make(chan asyncJob, asyncQueueSize),
		enqueueOnStart: true,
		workerOwner:    newID(),
		active:         map[string]context.CancelFunc{},
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
	EventSeq   uint64
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

// SetEvalCases replaces an agent's offline-eval case set.
func (h *Handler) SetEvalCases(ctx context.Context, id identity.Identity, agent string, cases []eval.Case) (eventlog.Envelope, error) {
	return eval.NewHandler(h.log).WithNow(h.now).SetCases(ctx, id, agent, cases)
}

// RunEvals runs an agent's stored eval cases against a version (0 = latest) through
// the provider and returns a scored report — recording nothing.
func (h *Handler) RunEvals(ctx context.Context, id identity.Identity, agent string, version int) (eval.Report, error) {
	if err := id.Valid(); err != nil {
		return eval.Report{}, err
	}
	v, ok, err := eval.Read(ctx, h.store, id, agent)
	if err != nil {
		return eval.Report{}, err
	}
	if !ok || len(v.Cases) == 0 {
		return eval.Report{}, fmt.Errorf("agent-manager: no eval cases for agent %q", agent)
	}
	return eval.Run(ctx, h.store, h.reg, h.tools, id, agent, version, v.Cases)
}

// RunAgent invokes the named agent against prompt and records the run. version 0
// runs the latest config; a positive version pins a specific published version
// (the registry's history). A provider failure is a recorded "failed" run (returned
// with Status failed), not an API error; only an unknown agent / version /
// misconfigured provider returns an error.
func (h *Handler) RunAgent(ctx context.Context, id identity.Identity, agent, prompt string, version int) (RunResult, error) {
	if err := id.Valid(); err != nil {
		return RunResult{}, err
	}
	if version < 0 {
		return RunResult{}, fmt.Errorf("agent-manager: version must be non-negative")
	}
	var out agents.Outcome
	var err error
	if version <= 0 {
		out, err = agents.InvokeWithTools(ctx, h.store, h.reg, h.tools, id, agent, prompt)
	} else {
		cfg, ok, cerr := agents.ReadConfig(ctx, h.store, id, agent, version)
		if cerr != nil {
			return RunResult{}, cerr
		}
		if !ok {
			return RunResult{}, fmt.Errorf("agent-manager: unknown agent %q version %d", agent, version)
		}
		out, err = agents.InvokeConfig(ctx, h.reg, h.tools, id, cfg, prompt)
	}
	if err != nil {
		return RunResult{}, err
	}
	runID := h.newID()
	event, err := h.append(ctx, id, events.TypeAgentRunRecorded, events.AgentRunRecorded{
		RunID: runID, Agent: agent, Model: out.Model, Prompt: prompt,
		Status: string(out.Status), Text: out.Text, Structured: out.Structured, ToolCalls: out.ToolCalls, Error: out.Error,
		PromptTokens: out.Usage.PromptTokens, CompletionTokens: out.Usage.CompletionTokens, At: h.now(),
	})
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{
		RunID: runID, Status: out.Status, Text: out.Text, Structured: out.Structured,
		Error: out.Error, EventSeq: event.Seq,
	}, nil
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
	event, err := h.append(ctx, id, events.TypeAgentRunRecorded, events.AgentRunRecorded{
		RunID: runID, Agent: agent, Model: out.Model, Prompt: prompt,
		Status: string(out.Status), Text: out.Text, Structured: out.Structured, ToolCalls: out.ToolCalls, Error: out.Error,
		PromptTokens: out.Usage.PromptTokens, CompletionTokens: out.Usage.CompletionTokens, At: h.now(),
	})
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{
		RunID: runID, Status: out.Status, Text: out.Text, Structured: out.Structured,
		Error: out.Error, EventSeq: event.Seq,
	}, nil
}

// StartRun accepts an agent run for asynchronous execution: it records an
// AgentRunStarted (status "running") and queues the work, returning the run id
// immediately. A worker later invokes the provider and records the terminal
// AgentRunRecorded; callers poll GET /v1/agent-runs/{run_id} for the outcome. An
// unknown agent is rejected up front (the read model is the same one RunAgent uses).
func (h *Handler) StartRun(ctx context.Context, id identity.Identity, agent, prompt string) (string, error) {
	runID, _, err := h.StartRunWithOptions(ctx, id, agent, prompt, AsyncRunOptions{})
	return runID, err
}

// AsyncRunOptions is the bounded, persisted execution and tracking contract for
// an asynchronous run. The idempotency key is hashed and used as an event-log
// claim; it is never written in plaintext.
type AsyncRunOptions struct {
	Version           int
	Timeout           time.Duration
	MaxAttempts       int
	IdempotencyKey    string
	BusinessReference string
	CorrelationID     string
}

// StartRunWithOptions durably accepts a run before offering a local wake-up. A
// saturated or API-only process never loses accepted work: worker pollers read it
// from the shared event log.
func (h *Handler) StartRunWithOptions(
	ctx context.Context,
	id identity.Identity,
	agent, prompt string,
	options AsyncRunOptions,
) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if _, ok, err := agents.Read(ctx, h.store, id, agent); err != nil {
		return "", eventlog.Envelope{}, err
	} else if !ok {
		return "", eventlog.Envelope{}, fmt.Errorf("agent-manager: unknown agent %q", agent)
	}
	options.IdempotencyKey = strings.TrimSpace(options.IdempotencyKey)
	options.BusinessReference = strings.TrimSpace(options.BusinessReference)
	options.CorrelationID = strings.TrimSpace(options.CorrelationID)
	if len(options.IdempotencyKey) > 256 ||
		len(options.BusinessReference) > 256 ||
		len(options.CorrelationID) > 256 {
		return "", eventlog.Envelope{}, fmt.Errorf("agent-manager: idempotency and tracking values are limited to 256 bytes")
	}
	if options.Version < 0 {
		return "", eventlog.Envelope{}, fmt.Errorf("agent-manager: version must be non-negative")
	}
	if options.Timeout == 0 {
		options.Timeout = defaultRunTimeout
	}
	if options.Timeout <= 0 || options.Timeout > maxRunTimeout {
		return "", eventlog.Envelope{}, fmt.Errorf("agent-manager: timeout must be between 1ns and %s", maxRunTimeout)
	}
	if options.MaxAttempts == 0 {
		options.MaxAttempts = defaultRunMaxAttempts
	}
	if options.MaxAttempts < 1 || options.MaxAttempts > maxRunAttempts {
		return "", eventlog.Envelope{}, fmt.Errorf("agent-manager: max_attempts must be between 1 and %d", maxRunAttempts)
	}
	requestHash := agentRunRequestHash(agent, prompt, options)
	keyHash := hashAgentRunKey(options.IdempotencyKey)
	if keyHash != "" {
		if existing, found, err := h.findIdempotentRun(ctx, id, agent, keyHash, requestHash); err != nil {
			return "", eventlog.Envelope{}, err
		} else if found {
			return existing.RunID, existing.Envelope, nil
		}
	}
	runID := h.newID()
	payload := events.AgentRunStarted{
		RunID: runID, Agent: agent, Prompt: prompt, Version: options.Version,
		TimeoutMS: options.Timeout.Milliseconds(), MaxAttempts: options.MaxAttempts,
		IdempotencyKeyHash: keyHash, RequestHash: requestHash,
		BusinessReference: options.BusinessReference, CorrelationID: options.CorrelationID,
		At: h.now(),
	}
	event, err := h.appendUnique(
		ctx, id, events.TypeAgentRunStarted, payload,
		agentRunIdempotencyClaim(agent, keyHash),
	)
	if err != nil {
		if errors.Is(err, eventlog.ErrConflict) && keyHash != "" {
			if existing, found, findErr := h.findIdempotentRun(ctx, id, agent, keyHash, requestHash); findErr != nil {
				return "", eventlog.Envelope{}, findErr
			} else if found {
				return existing.RunID, existing.Envelope, nil
			}
		}
		return "", eventlog.Envelope{}, err
	}
	if h.enqueueOnStart {
		h.enqueue(asyncJob{
			id: id, runID: runID, agent: agent, prompt: prompt, version: options.Version,
		})
	}
	return runID, event, nil
}

// StartWorkers launches n goroutines that drain the async run queue until ctx is
// cancelled; an in-flight run finishes (so shutdown never corrupts it into a
// failure) while a queued-but-unstarted run stays "running" and is recovered on
// the next boot. Pair with DrainWorkers to wait for them before closing the log.
func (h *Handler) StartWorkers(ctx context.Context, n int) {
	if n <= 0 {
		return
	}
	h.wg.Add(1)
	go h.pollRuns(ctx)
	for i := 0; i < n; i++ {
		h.wg.Add(1)
		go h.worker(ctx)
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
			h.process(ctx, job)
		}
	}
}

func (h *Handler) pollRuns(ctx context.Context) {
	defer h.wg.Done()
	ticker := time.NewTicker(runPollInterval)
	defer ticker.Stop()
	for {
		if _, err := h.RecoverRunning(ctx); err != nil && ctx.Err() == nil {
			slog.Error("agent-manager: scan durable run queue", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) enqueue(job asyncJob) bool {
	select {
	case h.jobs <- job:
		return true
	default:
		return false
	}
}

// process claims one durable attempt before invoking the provider. Duplicate
// local wake-ups and replica scans are harmless: only one claim append succeeds.
func (h *Handler) process(ctx context.Context, job asyncJob) {
	state, found, err := h.foldAsyncRun(ctx, job.id, job.runID)
	if err != nil {
		slog.Error("agent-manager: fold async run", "run_id", job.runID, "err", err)
		return
	}
	if !found || state.completed {
		return
	}
	if state.cancelRequested {
		attempt := max(1, state.terminalAttempt+1, state.claim.Attempt)
		if _, err := h.appendUnique(ctx, job.id, events.TypeAgentRunRecorded, events.AgentRunRecorded{
			RunID: job.runID, Agent: job.agent, Prompt: job.prompt,
			Status: string(domain.RunCancelled), Error: "cancelled by " + state.cancelActor,
			Attempt: attempt, At: h.now(),
		}, fmt.Sprintf("agent.run.terminal\x00%s\x00%d", job.runID, attempt)); err != nil &&
			!errors.Is(err, eventlog.ErrConflict) {
			slog.Error("agent-manager: record cancellation", "run_id", job.runID, "err", err)
		}
		return
	}
	if state.claim.Attempt > 0 && state.terminalAttempt < state.claim.Attempt {
		if state.leaseUntil.After(h.now()) {
			return
		}
		if err := h.deadLetterIndeterminate(ctx, job.id, state); err != nil &&
			!errors.Is(err, eventlog.ErrConflict) {
			slog.Error("agent-manager: dead-letter expired attempt", "run_id", job.runID, "err", err)
		}
		return
	}
	attempt := state.terminalAttempt + 1
	if attempt > state.started.MaxAttempts {
		if err := h.deadLetter(ctx, job.id, state, attempt,
			fmt.Sprintf("maximum attempts %d exhausted", state.started.MaxAttempts)); err != nil &&
			!errors.Is(err, eventlog.ErrConflict) {
			slog.Error("agent-manager: record exhausted dead letter", "run_id", job.runID, "err", err)
		}
		return
	}
	if state.terminalAttempt > 0 && !state.retryRequested {
		return
	}
	claim := events.AgentRunClaimed{
		RunID: job.runID, Owner: h.workerOwner, Attempt: attempt,
		LeaseUntil: h.now().Add(runLease),
	}
	if _, err := h.appendUnique(
		ctx, job.id, events.TypeAgentRunClaimed, claim,
		fmt.Sprintf("agent.run.claim\x00%s\x00%d", job.runID, attempt),
	); err != nil {
		if !errors.Is(err, eventlog.ErrConflict) {
			slog.Error("agent-manager: claim async run", "run_id", job.runID, "err", err)
		}
		return
	}
	timeout := time.Duration(state.started.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultRunTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	runCtx, err = effect.WithRequest(runCtx, effect.Request{Key: job.runID, Attempt: attempt})
	if err != nil {
		cancel()
		slog.Error("agent-manager: build effect context", "run_id", job.runID, "err", err)
		return
	}
	h.activeMu.Lock()
	h.active[job.runID] = cancel
	h.activeMu.Unlock()
	defer func() {
		h.activeMu.Lock()
		delete(h.active, job.runID)
		h.activeMu.Unlock()
	}()
	heartbeatDone := make(chan error, 1)
	go h.heartbeatRun(runCtx, cancel, job.id, job.runID, attempt, heartbeatDone)
	var out agents.Outcome
	if state.started.Version <= 0 {
		out, err = agents.InvokeWithTools(runCtx, h.store, h.reg, h.tools, job.id, job.agent, job.prompt)
	} else {
		var cfg agents.AgentConfig
		var ok bool
		cfg, ok, err = agents.ReadConfig(runCtx, h.store, job.id, job.agent, state.started.Version)
		if err == nil && !ok {
			err = fmt.Errorf("agent-manager: unknown agent %q version %d", job.agent, state.started.Version)
		}
		if err == nil {
			out, err = agents.InvokeConfig(runCtx, h.reg, h.tools, job.id, cfg, job.prompt)
		}
	}
	cancel()
	heartbeatErr := <-heartbeatDone
	if heartbeatErr != nil {
		// Once lease extension fails, the process can no longer prove that it
		// owns the attempt. Its provider outcome is therefore indeterminate and
		// must not race a successor's terminal record.
		slog.Error(
			"agent-manager: lost async run lease",
			"run_id", job.runID,
			"attempt", attempt,
			"err", heartbeatErr,
		)
		return
	}
	status := out.Status
	errorText := out.Error
	if err != nil {
		status, errorText = domain.RunFailed, err.Error()
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		status, errorText = domain.RunTimedOut, "agent run timed out"
	}
	latest, _, foldErr := h.foldAsyncRun(context.Background(), job.id, job.runID)
	if foldErr != nil {
		slog.Error("agent-manager: check cancellation", "run_id", job.runID, "err", foldErr)
		return
	}
	if latest.cancelRequested {
		status, errorText = domain.RunCancelled, "cancelled by "+latest.cancelActor
	}
	if ctx.Err() != nil && !latest.cancelRequested {
		// Shutdown or worker loss leaves an indeterminate claimed attempt. A
		// successor dead-letters it after lease expiry rather than inventing a
		// provider failure or duplicating an at-least-once call.
		return
	}
	if _, appendErr := h.appendUnique(context.Background(), job.id, events.TypeAgentRunRecorded, events.AgentRunRecorded{
		RunID: job.runID, Agent: job.agent, Model: out.Model, Prompt: job.prompt,
		Status: string(status), Text: out.Text, Structured: out.Structured,
		ToolCalls: out.ToolCalls, Error: errorText,
		PromptTokens: out.Usage.PromptTokens, CompletionTokens: out.Usage.CompletionTokens,
		Attempt: attempt, At: h.now(),
	}, fmt.Sprintf("agent.run.terminal\x00%s\x00%d", job.runID, attempt)); appendErr != nil &&
		!errors.Is(appendErr, eventlog.ErrConflict) {
		slog.Error("agent-manager: failed to record async run", "run_id", job.runID, "err", appendErr)
	}
}

func (h *Handler) heartbeatRun(
	ctx context.Context,
	cancel context.CancelFunc,
	id identity.Identity,
	runID string,
	attempt int,
	done chan<- error,
) {
	ticker := time.NewTicker(runLease / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case <-ticker.C:
			state, found, err := h.foldAsyncRun(ctx, id, runID)
			if err != nil {
				cancel()
				done <- fmt.Errorf("fold durable state: %w", err)
				return
			}
			if !found {
				cancel()
				done <- fmt.Errorf("durable run disappeared")
				return
			}
			if state.cancelRequested {
				cancel()
				done <- nil
				return
			}
			if _, err := h.append(ctx, id, events.TypeAgentRunHeartbeat, events.AgentRunHeartbeat{
				RunID: runID, Owner: h.workerOwner, Attempt: attempt,
				LeaseUntil: h.now().Add(runLease),
			}); err != nil {
				cancel()
				done <- fmt.Errorf("append heartbeat: %w", err)
				return
			}
		}
	}
}

// RecoverRunning scans the durable source of truth across tenants and offers
// runnable items to this process's bounded local queue. Claims in process(), not
// queue membership, own work across replicas.
func (h *Handler) RecoverRunning(ctx context.Context) (int, error) {
	evs, err := h.log.Read(ctx, 0)
	if err != nil {
		return 0, fmt.Errorf("agent-manager: read log: %w", err)
	}
	type runKey struct {
		org, workspace, runID string
	}
	started := map[runKey]asyncJob{}
	terminal := map[runKey]bool{}
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
			key := runKey{org: e.Org, workspace: e.Workspace, runID: p.RunID}
			started[key] = asyncJob{
				id:    identity.Identity{Org: e.Org, Workspace: e.Workspace, Actor: e.Actor},
				runID: p.RunID, agent: p.Agent, prompt: p.Prompt, version: p.Version,
			}
		case events.TypeAgentRunRecorded:
			var p events.AgentRunRecorded
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return 0, fmt.Errorf("agent-manager: decode run_recorded seq %d: %w", e.Seq, err)
			}
			terminal[runKey{org: e.Org, workspace: e.Workspace, runID: p.RunID}] = true
		case events.TypeAgentRunDeadLettered:
			var p events.AgentRunDeadLettered
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return 0, fmt.Errorf("agent-manager: decode run_dead_lettered seq %d: %w", e.Seq, err)
			}
			terminal[runKey{org: e.Org, workspace: e.Workspace, runID: p.RunID}] = true
		case events.TypeAgentRunRetryRequested:
			var p events.AgentRunRetryRequested
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return 0, fmt.Errorf("agent-manager: decode retry_requested seq %d: %w", e.Seq, err)
			}
			terminal[runKey{org: e.Org, workspace: e.Workspace, runID: p.RunID}] = false
		}
	}
	enqueued := 0
	for key, p := range started {
		if terminal[key] {
			continue
		}
		p.runID = key.runID
		if h.enqueue(p) {
			enqueued++
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
	run, ok, err := h.runStateFromLog(ctx, id, cmd.RunID)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	if !ok {
		return "", eventlog.Envelope{}, fmt.Errorf("agent-manager: unknown run %q", cmd.RunID)
	}
	if cmd.Agent != "" && cmd.Agent != run.agent {
		return "", eventlog.Envelope{}, fmt.Errorf("agent-manager: run %q belongs to agent %q, not %q", cmd.RunID, run.agent, cmd.Agent)
	}
	if run.status != domain.RunCompleted {
		return "", eventlog.Envelope{}, fmt.Errorf("agent-manager: only a completed run can be escalated (run %q is %s)", cmd.RunID, run.status)
	}
	if caseID, e, found, err := h.escalatedCaseFromLog(ctx, id, cmd.RunID); err != nil {
		return "", eventlog.Envelope{}, err
	} else if found {
		return caseID, e, nil
	}
	caseID := h.newID()
	source, err := json.Marshal(map[string]string{"source": "agent", "agent": run.agent, "run_id": cmd.RunID})
	if err != nil {
		return "", eventlog.Envelope{}, fmt.Errorf("agent-manager: marshal escalation context: %w", err)
	}
	// An agent escalation opens a dedicated agent_review case (per the journeys doc) so
	// these are filterable apart from flow manual-reviews; an explicit type still wins.
	caseType := cmd.CaseType
	if caseType == "" {
		caseType = "agent_review"
	}
	payload, err := json.Marshal(caseevents.ReviewRequested{
		CaseID: caseID, CompanyName: cmd.CompanyName, CaseType: caseType, SLADays: cmd.SLADays, Context: source,
	})
	if err != nil {
		return "", eventlog.Envelope{}, fmt.Errorf("agent-manager: marshal review request: %w", err)
	}
	e, err := h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: caseevents.StreamCases, Type: caseevents.TypeReviewRequested,
		Time: h.now(), Payload: payload, Unique: escalationClaim(cmd.RunID),
	})
	if err != nil {
		if errors.Is(err, eventlog.ErrConflict) {
			existingID, existing, found, readErr := h.escalatedCaseFromLog(ctx, id, cmd.RunID)
			if readErr != nil {
				return "", eventlog.Envelope{}, readErr
			}
			if found {
				return existingID, existing, nil
			}
		}
		return "", eventlog.Envelope{}, err
	}
	return caseID, e, nil
}

type runState struct {
	agent  string
	status domain.RunStatus
}

// runStateFromLog resolves the immediately consistent source-of-truth state of
// one tenant run. Escalation is a command decision, so it must not depend on
// projection timing.
func (h *Handler) runStateFromLog(ctx context.Context, id identity.Identity, runID string) (runState, bool, error) {
	evs, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, events.StreamAgents, 0)
	if err != nil {
		return runState{}, false, fmt.Errorf("agent-manager: read agent stream: %w", err)
	}
	var out runState
	found := false
	for _, e := range evs {
		switch e.Type {
		case events.TypeAgentRunStarted:
			var p events.AgentRunStarted
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return runState{}, false, fmt.Errorf("agent-manager: decode run_started seq %d: %w", e.Seq, err)
			}
			if p.RunID == runID {
				out, found = runState{agent: p.Agent, status: domain.RunRunning}, true
			}
		case events.TypeAgentRunRecorded:
			var p events.AgentRunRecorded
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return runState{}, false, fmt.Errorf("agent-manager: decode run_recorded seq %d: %w", e.Seq, err)
			}
			if p.RunID == runID {
				status, valid := domain.ParseRunStatus(p.Status)
				if !valid {
					return runState{}, false, fmt.Errorf("agent-manager: run %q has unknown status %q at seq %d", runID, p.Status, e.Seq)
				}
				out, found = runState{agent: p.Agent, status: status}, true
			}
		}
	}
	return out, found, nil
}

func (h *Handler) escalatedCaseFromLog(ctx context.Context, id identity.Identity, runID string) (string, eventlog.Envelope, bool, error) {
	evs, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, caseevents.StreamCases, 0)
	if err != nil {
		return "", eventlog.Envelope{}, false, fmt.Errorf("agent-manager: read case stream: %w", err)
	}
	for _, e := range evs {
		if e.Type != caseevents.TypeReviewRequested {
			continue
		}
		var p caseevents.ReviewRequested
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return "", eventlog.Envelope{}, false, fmt.Errorf("agent-manager: decode review_requested seq %d: %w", e.Seq, err)
		}
		var source struct {
			Source string `json:"source"`
			RunID  string `json:"run_id"`
		}
		if len(p.Context) > 0 {
			if err := json.Unmarshal(p.Context, &source); err != nil {
				return "", eventlog.Envelope{}, false, fmt.Errorf("agent-manager: decode review context seq %d: %w", e.Seq, err)
			}
		}
		if source.Source == "agent" && source.RunID == runID {
			return p.CaseID, e, true, nil
		}
	}
	return "", eventlog.Envelope{}, false, nil
}

func escalationClaim(runID string) string { return "agent.escalate\x00" + runID }

type idempotentRun struct {
	RunID    string
	Envelope eventlog.Envelope
}

func agentRunRequestHash(agent, prompt string, options AsyncRunOptions) string {
	encoded, err := json.Marshal(struct {
		Agent             string `json:"agent"`
		Prompt            string `json:"prompt"`
		Version           int    `json:"version,omitempty"`
		TimeoutMS         int64  `json:"timeout_ms"`
		MaxAttempts       int    `json:"max_attempts"`
		BusinessReference string `json:"business_reference,omitempty"`
		CorrelationID     string `json:"correlation_id,omitempty"`
	}{
		Agent: agent, Prompt: prompt, Version: options.Version,
		TimeoutMS: options.Timeout.Milliseconds(), MaxAttempts: options.MaxAttempts,
		BusinessReference: options.BusinessReference, CorrelationID: options.CorrelationID,
	})
	if err != nil {
		panic("agent-manager: fixed run request cannot be marshaled: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func hashAgentRunKey(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func agentRunIdempotencyClaim(agent, keyHash string) string {
	if keyHash == "" {
		return ""
	}
	return "agent.run.idempotency\x00" + agent + "\x00" + keyHash
}

func (h *Handler) findIdempotentRun(
	ctx context.Context,
	id identity.Identity,
	agent, keyHash, requestHash string,
) (idempotentRun, bool, error) {
	envelopes, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, events.StreamAgents, 0)
	if err != nil {
		return idempotentRun{}, false, err
	}
	for _, envelope := range envelopes {
		if envelope.Type != events.TypeAgentRunStarted {
			continue
		}
		var started events.AgentRunStarted
		if err := json.Unmarshal(envelope.Payload, &started); err != nil {
			return idempotentRun{}, false, fmt.Errorf("agent-manager: decode run start seq %d: %w", envelope.Seq, err)
		}
		if started.Agent != agent || started.IdempotencyKeyHash != keyHash {
			continue
		}
		if started.RequestHash != requestHash {
			return idempotentRun{}, true, fmt.Errorf(
				"agent-manager: idempotency key was already used with a different request",
			)
		}
		return idempotentRun{RunID: started.RunID, Envelope: envelope}, true, nil
	}
	return idempotentRun{}, false, nil
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ string, payload any) (eventlog.Envelope, error) {
	return eventlog.AppendJSON(ctx, h.log, id.Org, id.Workspace, id.Actor, events.StreamAgents, typ, h.now(), payload)
}

func (h *Handler) appendUnique(
	ctx context.Context,
	id identity.Identity,
	typ string,
	payload any,
	unique string,
) (eventlog.Envelope, error) {
	return eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor,
		events.StreamAgents, typ, h.now(), payload, unique,
	)
}

func newID() string {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic("agent-manager: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
