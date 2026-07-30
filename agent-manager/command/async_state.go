// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/intraktible/agent-manager/domain"
	"github.com/e6qu/intraktible/agent-manager/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

type asyncRunState struct {
	started         events.AgentRunStarted
	claim           events.AgentRunClaimed
	leaseUntil      time.Time
	terminalAttempt int
	terminalStatus  domain.RunStatus
	completed       bool
	retryRequested  bool
	cancelRequested bool
	cancelActor     string
}

func (h *Handler) foldAsyncRun(
	ctx context.Context,
	id identity.Identity,
	runID string,
) (asyncRunState, bool, error) {
	envelopes, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, events.StreamAgents, 0)
	if err != nil {
		return asyncRunState{}, false, fmt.Errorf("agent-manager: read run state: %w", err)
	}
	var state asyncRunState
	found := false
	for _, envelope := range envelopes {
		switch envelope.Type {
		case events.TypeAgentRunStarted:
			var started events.AgentRunStarted
			if err := json.Unmarshal(envelope.Payload, &started); err != nil {
				return asyncRunState{}, false, fmt.Errorf("agent-manager: decode run start seq %d: %w", envelope.Seq, err)
			}
			if started.RunID != runID {
				continue
			}
			if started.MaxAttempts == 0 {
				started.MaxAttempts = defaultRunMaxAttempts
			}
			if started.TimeoutMS == 0 {
				started.TimeoutMS = defaultRunTimeout.Milliseconds()
			}
			state.started, found = started, true
		case events.TypeAgentRunClaimed:
			var claim events.AgentRunClaimed
			if err := json.Unmarshal(envelope.Payload, &claim); err != nil {
				return asyncRunState{}, false, fmt.Errorf("agent-manager: decode run claim seq %d: %w", envelope.Seq, err)
			}
			if claim.RunID == runID && claim.Attempt >= state.claim.Attempt {
				state.claim, state.leaseUntil = claim, claim.LeaseUntil
				state.retryRequested = false
			}
		case events.TypeAgentRunHeartbeat:
			var heartbeat events.AgentRunHeartbeat
			if err := json.Unmarshal(envelope.Payload, &heartbeat); err != nil {
				return asyncRunState{}, false, fmt.Errorf("agent-manager: decode run heartbeat seq %d: %w", envelope.Seq, err)
			}
			if heartbeat.RunID == runID &&
				heartbeat.Attempt == state.claim.Attempt &&
				heartbeat.Owner == state.claim.Owner &&
				heartbeat.LeaseUntil.After(state.leaseUntil) {
				state.leaseUntil = heartbeat.LeaseUntil
			}
		case events.TypeAgentRunRecorded:
			var recorded events.AgentRunRecorded
			if err := json.Unmarshal(envelope.Payload, &recorded); err != nil {
				return asyncRunState{}, false, fmt.Errorf("agent-manager: decode run outcome seq %d: %w", envelope.Seq, err)
			}
			if recorded.RunID != runID {
				continue
			}
			status, ok := domain.ParseRunStatus(recorded.Status)
			if !ok {
				return asyncRunState{}, false, fmt.Errorf(
					"agent-manager: run %q has invalid status %q",
					runID, recorded.Status,
				)
			}
			recordedAttempt := recorded.Attempt
			if recordedAttempt == 0 {
				recordedAttempt = max(1, state.claim.Attempt)
			}
			if recordedAttempt >= state.terminalAttempt {
				state.terminalAttempt, state.terminalStatus = recordedAttempt, status
				state.completed = status == domain.RunCompleted
				state.retryRequested = false
			}
		case events.TypeAgentRunDeadLettered:
			var dead events.AgentRunDeadLettered
			if err := json.Unmarshal(envelope.Payload, &dead); err != nil {
				return asyncRunState{}, false, fmt.Errorf("agent-manager: decode dead letter seq %d: %w", envelope.Seq, err)
			}
			if dead.RunID == runID && dead.Attempt >= state.terminalAttempt {
				state.terminalAttempt = dead.Attempt
				state.terminalStatus = domain.RunDeadLetter
				state.retryRequested = false
			}
		case events.TypeAgentRunRetryRequested:
			var retry events.AgentRunRetryRequested
			if err := json.Unmarshal(envelope.Payload, &retry); err != nil {
				return asyncRunState{}, false, fmt.Errorf("agent-manager: decode retry seq %d: %w", envelope.Seq, err)
			}
			if retry.RunID == runID {
				state.retryRequested = true
				state.completed = false
				state.cancelRequested = false
			}
		case events.TypeAgentRunCancelRequested:
			var cancel events.AgentRunCancelRequested
			if err := json.Unmarshal(envelope.Payload, &cancel); err != nil {
				return asyncRunState{}, false, fmt.Errorf("agent-manager: decode cancellation seq %d: %w", envelope.Seq, err)
			}
			if cancel.RunID == runID {
				state.cancelRequested, state.cancelActor = true, cancel.Actor
			}
		}
	}
	return state, found, nil
}

func (h *Handler) deadLetterIndeterminate(
	ctx context.Context,
	id identity.Identity,
	state asyncRunState,
) error {
	return h.deadLetter(
		ctx, id, state, state.claim.Attempt,
		fmt.Sprintf(
			"worker %s lost its lease after an at-least-once provider call; outcome is indeterminate and automatic replay was refused",
			state.claim.Owner,
		),
	)
}

func (h *Handler) deadLetter(
	ctx context.Context,
	id identity.Identity,
	state asyncRunState,
	attempt int,
	reason string,
) error {
	_, err := h.appendUnique(ctx, id, events.TypeAgentRunDeadLettered, events.AgentRunDeadLettered{
		RunID: state.started.RunID, Agent: state.started.Agent, Prompt: state.started.Prompt,
		Attempt: attempt, Error: reason, At: h.now(),
	}, fmt.Sprintf("agent.run.terminal\x00%s\x00%d", state.started.RunID, attempt))
	return err
}

// CancelRun requests cancellation of a queued or active asynchronous run. The
// active owner's heartbeat loop observes it and cancels the provider context.
func (h *Handler) CancelRun(
	ctx context.Context,
	id identity.Identity,
	runID string,
) (eventlog.Envelope, error) {
	state, found, err := h.foldAsyncRun(ctx, id, runID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !found {
		return eventlog.Envelope{}, fmt.Errorf("agent-manager: unknown run %q", runID)
	}
	if state.completed || (state.terminalAttempt > 0 && !state.retryRequested) {
		return eventlog.Envelope{}, fmt.Errorf(
			"agent-manager: run %q is already %s",
			runID, state.terminalStatus,
		)
	}
	envelope, err := h.appendUnique(ctx, id, events.TypeAgentRunCancelRequested,
		events.AgentRunCancelRequested{RunID: runID, Actor: id.Actor, At: h.now()},
		"agent.run.cancel\x00"+runID,
	)
	if err == nil {
		h.activeMu.Lock()
		cancel := h.active[runID]
		h.activeMu.Unlock()
		if cancel != nil {
			cancel()
		}
		h.enqueue(asyncJob{
			id: id, runID: runID, agent: state.started.Agent,
			prompt: state.started.Prompt, version: state.started.Version,
		})
	}
	return envelope, err
}

// RetryRun reopens a terminal non-successful logical run for its next bounded
// attempt. Dead-letter retries require an explicit at-least-once acknowledgement.
func (h *Handler) RetryRun(
	ctx context.Context,
	id identity.Identity,
	runID, reason string,
	acknowledgeAtLeastOnce bool,
) (eventlog.Envelope, error) {
	state, found, err := h.foldAsyncRun(ctx, id, runID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !found {
		return eventlog.Envelope{}, fmt.Errorf("agent-manager: unknown run %q", runID)
	}
	if state.completed {
		return eventlog.Envelope{}, fmt.Errorf("agent-manager: completed run %q cannot be retried", runID)
	}
	if state.terminalAttempt == 0 {
		return eventlog.Envelope{}, fmt.Errorf("agent-manager: run %q is still active", runID)
	}
	if state.retryRequested {
		return eventlog.Envelope{}, fmt.Errorf("agent-manager: run %q already has a retry pending", runID)
	}
	if state.terminalAttempt >= state.started.MaxAttempts {
		return eventlog.Envelope{}, fmt.Errorf(
			"agent-manager: run %q exhausted max_attempts=%d",
			runID, state.started.MaxAttempts,
		)
	}
	if state.terminalStatus == domain.RunDeadLetter && !acknowledgeAtLeastOnce {
		return eventlog.Envelope{}, fmt.Errorf(
			"agent-manager: retrying dead-letter run %q requires acknowledge_at_least_once=true",
			runID,
		)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return eventlog.Envelope{}, fmt.Errorf("agent-manager: retry reason is required")
	}
	nextAttempt := state.terminalAttempt + 1
	envelope, err := h.appendUnique(ctx, id, events.TypeAgentRunRetryRequested,
		events.AgentRunRetryRequested{
			RunID: runID, Actor: id.Actor,
			AcknowledgeAtLeastOnce: acknowledgeAtLeastOnce,
			Reason:                 reason, At: h.now(),
		},
		fmt.Sprintf("agent.run.retry\x00%s\x00%d", runID, nextAttempt),
	)
	if err == nil {
		h.enqueue(asyncJob{
			id: id, runID: runID, agent: state.started.Agent,
			prompt: state.started.Prompt, version: state.started.Version,
		})
	}
	return envelope, err
}
