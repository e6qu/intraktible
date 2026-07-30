// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/decision-engine/preapproval"
	"github.com/e6qu/intraktible/platform/effect"
	"github.com/e6qu/intraktible/platform/entity"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

const (
	decisionRecoveryLease       = 30 * time.Second
	maxDecisionRecoveryAttempts = 3
)

// RecoverySummary reports one worker scan. Claimed counts generations this
// worker owned; Recovered and Abandoned are terminal outcomes of that work.
type RecoverySummary struct {
	Scanned   int `json:"scanned"`
	Claimed   int `json:"claimed"`
	Recovered int `json:"recovered"`
	Abandoned int `json:"abandoned"`
}

type decisionGeneration struct {
	id             identity.Identity
	started        events.DecisionStarted
	startedAt      time.Time
	generation     int
	boundary       int
	events         []eventlog.Envelope
	finalized      bool
	terminal       *eventlog.Envelope
	claim          events.DecisionRecoveryClaimed
	leaseUntil     time.Time
	interruptedErr string
}

// RecoverInterrupted scans the append-only source of truth and claims unfinished
// invocation generations. It is safe for several worker replicas to call: one
// permanent claim key wins each attempt, and an unexpired lease suppresses the
// next attempt.
func (h *DecideHandler) RecoverInterrupted(ctx context.Context, owner string) (RecoverySummary, error) {
	if owner == "" {
		return RecoverySummary{}, fmt.Errorf("decision-engine: recovery owner is required")
	}
	all, err := h.log.Read(ctx, 0)
	if err != nil {
		return RecoverySummary{}, fmt.Errorf("decision-engine: read recovery log: %w", err)
	}
	work, err := collectDecisionGenerations(all)
	if err != nil {
		return RecoverySummary{}, err
	}
	summary := RecoverySummary{Scanned: len(work)}
	for _, generation := range work {
		if generation.finalized || generation.started.DecisionID == "" {
			continue
		}
		now := h.now()
		if generation.leaseUntil.After(now) {
			continue
		}
		attempt := generation.claim.Attempt + 1
		if attempt > maxDecisionRecoveryAttempts {
			claimed, claimErr := h.claimDecisionRecovery(ctx, generation, owner, attempt)
			if claimErr != nil {
				if errors.Is(claimErr, eventlog.ErrConflict) {
					continue
				}
				return summary, claimErr
			}
			if !claimed {
				continue
			}
			summary.Claimed++
			if err := h.abandonDecision(
				ctx, generation, attempt,
				fmt.Sprintf("recovery exhausted after %d attempts", maxDecisionRecoveryAttempts),
				"",
			); err != nil {
				return summary, err
			}
			summary.Abandoned++
			continue
		}
		claimed, err := h.claimDecisionRecovery(ctx, generation, owner, attempt)
		if err != nil {
			if errors.Is(err, eventlog.ErrConflict) {
				continue
			}
			return summary, err
		}
		if !claimed {
			continue
		}
		summary.Claimed++
		outcome, err := h.recoverWithHeartbeat(ctx, generation, owner, attempt)
		if err != nil {
			return summary, err
		}
		if outcome == "abandoned" {
			summary.Abandoned++
		} else {
			summary.Recovered++
		}
	}
	return summary, nil
}

func (h *DecideHandler) recoverWithHeartbeat(
	ctx context.Context,
	generation decisionGeneration,
	owner string,
	attempt int,
) (string, error) {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(decisionRecoveryLease / 3)
		defer ticker.Stop()
		defer close(done)
		for {
			select {
			case <-workCtx.Done():
				return
			case <-ticker.C:
				_, err := h.emitEnvelope(workCtx, generation.id, events.TypeRecoveryHeartbeat,
					events.DecisionRecoveryHeartbeat{
						DecisionID: generation.started.DecisionID,
						Generation: generation.generation,
						Owner:      owner,
						Attempt:    attempt,
						LeaseUntil: h.now().Add(decisionRecoveryLease),
					})
				if err != nil {
					select {
					case heartbeatErr <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()

	outcome, recoveryErr := h.recoverDecisionGeneration(workCtx, generation, attempt)
	cancel()
	<-done
	select {
	case err := <-heartbeatErr:
		if recoveryErr == nil {
			recoveryErr = fmt.Errorf("decision-engine: recovery heartbeat: %w", err)
		}
	default:
	}
	return outcome, recoveryErr
}

func collectDecisionGenerations(all []eventlog.Envelope) ([]decisionGeneration, error) {
	type decisionKey struct {
		org, workspace, decisionID string
	}
	byID := map[decisionKey][]eventlog.Envelope{}
	order := []decisionKey{}
	for _, envelope := range all {
		if envelope.Stream != events.StreamDecisions {
			continue
		}
		decisionID, relevant, err := decisionEventID(envelope)
		if err != nil {
			return nil, err
		}
		if !relevant || decisionID == "" {
			continue
		}
		key := decisionKey{
			org: envelope.Org, workspace: envelope.Workspace, decisionID: decisionID,
		}
		if _, exists := byID[key]; !exists {
			order = append(order, key)
		}
		byID[key] = append(byID[key], envelope)
	}
	out := make([]decisionGeneration, 0, len(order))
	for _, key := range order {
		envelopes := byID[key]
		var generation decisionGeneration
		generation.events = envelopes
		for i, envelope := range envelopes {
			switch envelope.Type {
			case events.TypeDecisionStarted:
				if err := json.Unmarshal(envelope.Payload, &generation.started); err != nil {
					return nil, fmt.Errorf("decision-engine: decode recovery start seq %d: %w", envelope.Seq, err)
				}
				generation.id = identity.Identity{
					Org: envelope.Org, Workspace: envelope.Workspace, Actor: envelope.Actor,
				}
				generation.startedAt, generation.generation, generation.boundary = envelope.Time, 1, i
				generation.leaseUntil = generation.started.RecoveryAfter
			case events.TypeDecisionResumed:
				var resumed events.DecisionResumed
				if err := json.Unmarshal(envelope.Payload, &resumed); err != nil {
					return nil, fmt.Errorf("decision-engine: decode resumed seq %d: %w", envelope.Seq, err)
				}
				generation.generation++
				generation.boundary = i
				generation.startedAt = envelope.Time
				generation.finalized, generation.terminal = false, nil
				generation.claim = events.DecisionRecoveryClaimed{}
				generation.leaseUntil = resumed.RecoveryAfter
				generation.interruptedErr = ""
			case events.TypeExecutionInterrupted:
				var interrupted events.DecisionExecutionInterrupted
				if err := json.Unmarshal(envelope.Payload, &interrupted); err != nil {
					return nil, fmt.Errorf("decision-engine: decode interruption seq %d: %w", envelope.Seq, err)
				}
				if interrupted.Generation == generation.generation && i >= generation.boundary {
					generation.leaseUntil = time.Time{}
					generation.interruptedErr = interrupted.Error
				}
			case events.TypeDecisionCompleted, events.TypeDecisionFailed,
				events.TypeDecisionSuspended, events.TypeDecisionAbandoned:
				if i >= generation.boundary {
					terminal := envelope
					generation.terminal = &terminal
				}
			case events.TypeDecisionFinalized:
				var finalized events.DecisionFinalized
				if err := json.Unmarshal(envelope.Payload, &finalized); err != nil {
					return nil, fmt.Errorf("decision-engine: decode finalized seq %d: %w", envelope.Seq, err)
				}
				recordedGeneration := finalized.Generation
				if recordedGeneration == 0 {
					recordedGeneration = 1
				}
				if recordedGeneration == generation.generation && i >= generation.boundary {
					generation.finalized = true
				}
			case events.TypeRecoveryClaimed:
				var claim events.DecisionRecoveryClaimed
				if err := json.Unmarshal(envelope.Payload, &claim); err != nil {
					return nil, fmt.Errorf("decision-engine: decode recovery claim seq %d: %w", envelope.Seq, err)
				}
				if claim.Generation == generation.generation && i >= generation.boundary &&
					claim.Attempt >= generation.claim.Attempt {
					generation.claim, generation.leaseUntil = claim, claim.LeaseUntil
				}
			case events.TypeRecoveryHeartbeat:
				var heartbeat events.DecisionRecoveryHeartbeat
				if err := json.Unmarshal(envelope.Payload, &heartbeat); err != nil {
					return nil, fmt.Errorf("decision-engine: decode recovery heartbeat seq %d: %w", envelope.Seq, err)
				}
				if heartbeat.Generation == generation.generation &&
					heartbeat.Attempt == generation.claim.Attempt &&
					heartbeat.Owner == generation.claim.Owner &&
					heartbeat.LeaseUntil.After(generation.leaseUntil) {
					generation.leaseUntil = heartbeat.LeaseUntil
				}
			}
		}
		out = append(out, generation)
	}
	return out, nil
}

func decisionEventID(envelope eventlog.Envelope) (string, bool, error) {
	var target struct {
		DecisionID string `json:"decision_id"`
	}
	switch envelope.Type {
	case events.TypeDecisionStarted, events.TypeContextPrepared, events.TypeNodeEvaluated,
		events.TypeEffectRequested, events.TypeEffectSucceeded, events.TypeEffectFailed,
		events.TypeDecisionCompleted, events.TypeDecisionFailed, events.TypeDecisionSuspended,
		events.TypeDecisionResumed, events.TypeDecisionAbandoned, events.TypeDecisionFinalized,
		events.TypeExecutionInterrupted,
		events.TypeRecoveryClaimed, events.TypeRecoveryHeartbeat, events.TypeManualReviewRequested,
		events.TypeShadowEvaluated:
	default:
		return "", false, nil
	}
	if err := json.Unmarshal(envelope.Payload, &target); err != nil {
		return "", true, fmt.Errorf(
			"decision-engine: decode %s decision id seq %d: %w",
			envelope.Type, envelope.Seq, err,
		)
	}
	return target.DecisionID, true, nil
}

func (h *DecideHandler) claimDecisionRecovery(
	ctx context.Context,
	generation decisionGeneration,
	owner string,
	attempt int,
) (bool, error) {
	previousErr := generation.interruptedErr
	if generation.claim.PreviousErr != "" {
		previousErr = generation.claim.PreviousErr
	}
	if generation.claim.Attempt > 0 {
		previousErr = fmt.Sprintf(
			"worker %s lost lease for attempt %d",
			generation.claim.Owner, generation.claim.Attempt,
		)
	}
	claim := events.DecisionRecoveryClaimed{
		DecisionID:  generation.started.DecisionID,
		Generation:  generation.generation,
		Owner:       owner,
		Attempt:     attempt,
		LeaseUntil:  h.now().Add(decisionRecoveryLease),
		PreviousErr: previousErr,
	}
	_, err := h.emitEnvelopeUnique(
		ctx, generation.id, events.TypeRecoveryClaimed, claim,
		decisionRecoveryClaim(generation.started.DecisionID, generation.generation, attempt),
	)
	return err == nil, err
}

func decisionRecoveryClaim(decisionID string, generation, attempt int) string {
	return fmt.Sprintf("decision.recovery\x00%s\x00%d\x00%d", decisionID, generation, attempt)
}

type recoveredEffect struct {
	request events.DecisionEffectRequested
	success *events.DecisionEffectSucceeded
	failed  *events.DecisionEffectFailed
}

type indeterminateEffectError struct {
	nodeID   string
	effectID string
}

func (e indeterminateEffectError) Error() string {
	return fmt.Sprintf(
		"effect %s at node %s has an indeterminate at-least-once provider outcome; automatic replay refused",
		e.effectID, e.nodeID,
	)
}

func (h *DecideHandler) recoverDecisionGeneration(
	ctx context.Context,
	generation decisionGeneration,
	attempt int,
) (string, error) {
	preApproved, err := isPreApprovalGeneration(generation)
	if err != nil {
		return "", err
	}
	if preApproved {
		if err := h.recoverPreApproval(ctx, generation); err != nil {
			return "", err
		}
		return "recovered", nil
	}
	noSideEffects, err := terminalNeedsNoSideEffects(generation)
	if err != nil {
		return "", err
	}
	if noSideEffects {
		if err := h.finalizeRecoveredGeneration(ctx, generation, generation.terminal.Seq); err != nil {
			return "", err
		}
		return "recovered", nil
	}
	fv, found, err := flows.Read(ctx, h.store, generation.id, generation.started.FlowID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf(
			"decision-engine: recover decision %q: flow %q not found",
			generation.started.DecisionID, generation.started.FlowID,
		)
	}
	version, ok := versionByNumber(fv, generation.started.Version)
	if !ok {
		return "", fmt.Errorf(
			"decision-engine: recover decision %q: flow version %d not found",
			generation.started.DecisionID, generation.started.Version,
		)
	}
	ref := EntityRef{
		Type: entity.Type(generation.started.EntityType),
		ID:   entity.ID(generation.started.EntityID),
	}
	state, rawData, baseData, err := h.recoveryState(ctx, generation, version, ref)
	if err != nil {
		return "", err
	}
	evidence, err := h.recoveryEffects(ctx, generation, ref, "live")
	if err != nil {
		return "", err
	}
	// Before yielded effects were introduced, DecisionStarted.Data was the
	// complete prepared record, including every eagerly resolved connector,
	// agent, and model namespace. RecoveryAfter was added in the same event
	// schema change, so its zero value is an explicit legacy-shape marker. Those
	// historical runs must replay their recorded values instead of calling a
	// provider that may have changed or no longer exist.
	legacyPreResolvedEffects := generation.started.RecoveryAfter.IsZero()
	governed := generation.started.Environment != string(domain.EnvSandbox)
	run, err := h.driveRecoveredExecution(
		ctx, generation.id, version.Graph, state, ref, governed,
		&effectAudit{
			decisionID: generation.started.DecisionID,
			flowID:     generation.started.FlowID,
			scope:      "live",
			version:    generation.started.Version,
			generation: generation.generation,
			ref:        ref,
		},
		evidence,
		legacyPreResolvedEffects,
		invocationTimeout(generation.started.Control),
	)
	var indeterminate indeterminateEffectError
	if errors.As(err, &indeterminate) {
		if abandonErr := h.abandonDecision(ctx, generation, attempt, err.Error(), indeterminate.nodeID); abandonErr != nil {
			return "", abandonErr
		}
		return "abandoned", nil
	}
	if err != nil {
		return "", err
	}
	selectedPolicy, err := h.recoveryPolicy(ctx, generation.id, generation.started)
	if err != nil {
		return "", err
	}
	resultSeq, err := h.recordRecoveredRun(
		ctx, generation, fv, ref, rawData, baseData, run, selectedPolicy,
	)
	if err != nil {
		return "", err
	}
	if err := h.finalizeRecoveredGeneration(ctx, generation, resultSeq); err != nil {
		return "", err
	}
	return "recovered", nil
}

func isPreApprovalGeneration(generation decisionGeneration) (bool, error) {
	if generation.started.PreApprovalID != "" {
		return true, nil
	}
	if generation.terminal == nil || generation.terminal.Type != events.TypeDecisionCompleted {
		return false, nil
	}
	var completed events.DecisionCompleted
	if err := json.Unmarshal(generation.terminal.Payload, &completed); err != nil {
		return false, fmt.Errorf("decision-engine: decode completed recovery terminal: %w", err)
	}
	return completed.PreApprovalID != "", nil
}

func terminalNeedsNoSideEffects(generation decisionGeneration) (bool, error) {
	if generation.terminal == nil {
		return false, nil
	}
	if generation.terminal.Type == events.TypeDecisionAbandoned {
		return true, nil
	}
	if generation.terminal.Type != events.TypeDecisionFailed {
		return false, nil
	}
	var failed events.DecisionFailed
	if err := json.Unmarshal(generation.terminal.Payload, &failed); err != nil {
		return false, fmt.Errorf("decision-engine: decode failed recovery terminal: %w", err)
	}
	return failed.NodeID == "prepare", nil
}

func (h *DecideHandler) recoverPreApproval(
	ctx context.Context,
	generation decisionGeneration,
) error {
	started := generation.started
	preApprovalID := started.PreApprovalID
	disposition := started.PreApprovalDisposition
	terms := started.PreApprovalTerms
	if generation.terminal != nil {
		if generation.terminal.Type != events.TypeDecisionCompleted {
			return fmt.Errorf(
				"decision-engine: pre-approved decision %q has terminal %s",
				started.DecisionID,
				generation.terminal.Type,
			)
		}
		var completed events.DecisionCompleted
		if err := json.Unmarshal(generation.terminal.Payload, &completed); err != nil {
			return err
		}
		if preApprovalID == "" {
			preApprovalID, disposition, terms = completed.PreApprovalID, completed.Disposition, completed.Output
		}
	}
	if preApprovalID == "" || len(terms) == 0 {
		return fmt.Errorf(
			"decision-engine: pre-approved decision %q lacks its durable grant snapshot",
			started.DecisionID,
		)
	}
	terminal, err := h.recordRecoveredTerminal(
		ctx,
		generation,
		events.TypeDecisionCompleted,
		events.DecisionCompleted{
			DecisionID: started.DecisionID,
			FlowID:     started.FlowID, Version: started.Version, Variant: started.Variant,
			Output: terms, Disposition: disposition,
			DispositionReason: "pre-approval honored", PreApprovalID: preApprovalID,
		},
	)
	if err != nil {
		return err
	}
	honored, err := h.appendStreamEnvelopeUnique(
		ctx,
		generation.id,
		preapproval.StreamPreApprovals,
		preapproval.TypeHonored,
		preapproval.Honored{
			PreApprovalID: preApprovalID,
			EntityType:    started.EntityType, EntityID: started.EntityID,
			DecisionID: started.DecisionID,
		},
		"preapproval.honored\x00"+started.DecisionID,
	)
	if err != nil && !errors.Is(err, eventlog.ErrConflict) {
		return err
	}
	return h.finalizeRecoveredGeneration(ctx, generation, max(terminal.Seq, honored.Seq))
}

func (h *DecideHandler) recoveryState(
	ctx context.Context,
	generation decisionGeneration,
	version flows.VersionView,
	ref EntityRef,
) (domain.ExecutionState, map[string]any, map[string]any, error) {
	rawJSON, err := h.openPII(ctx, generation.id, ref, generation.started.Data)
	if err != nil {
		return domain.ExecutionState{}, nil, nil, err
	}
	rawData := map[string]any{}
	if err := json.Unmarshal(rawJSON, &rawData); err != nil {
		return domain.ExecutionState{}, nil, nil, fmt.Errorf("decision-engine: recover raw input: %w", err)
	}
	if generation.generation > 1 {
		var (
			suspended events.DecisionSuspended
			resumed   events.DecisionResumed
		)
		for i := 0; i <= generation.boundary; i++ {
			switch generation.events[i].Type {
			case events.TypeDecisionSuspended:
				if err := json.Unmarshal(generation.events[i].Payload, &suspended); err != nil {
					return domain.ExecutionState{}, nil, nil, err
				}
			case events.TypeDecisionResumed:
				if i == generation.boundary {
					if err := json.Unmarshal(generation.events[i].Payload, &resumed); err != nil {
						return domain.ExecutionState{}, nil, nil, err
					}
				}
			}
		}
		stateJSON, err := h.openPII(ctx, generation.id, ref, suspended.State)
		if err != nil {
			return domain.ExecutionState{}, nil, nil, err
		}
		var suspend domain.SuspendState
		if err := json.Unmarshal(stateJSON, &suspend); err != nil {
			return domain.ExecutionState{}, nil, nil, fmt.Errorf("decision-engine: recover suspend state: %w", err)
		}
		outcomeJSON, err := h.openPII(ctx, generation.id, ref, resumed.Outcome)
		if err != nil {
			return domain.ExecutionState{}, nil, nil, err
		}
		outcome := map[string]any{}
		if err := json.Unmarshal(outcomeJSON, &outcome); err != nil {
			return domain.ExecutionState{}, nil, nil, fmt.Errorf("decision-engine: recover review outcome: %w", err)
		}
		return domain.ResumeExecution(suspend, outcome), rawData, suspend.Record, nil
	}

	var prepared events.DecisionContextPrepared
	foundPrepared := false
	for _, envelope := range generation.events[generation.boundary:] {
		if envelope.Type != events.TypeContextPrepared {
			continue
		}
		if err := json.Unmarshal(envelope.Payload, &prepared); err != nil {
			return domain.ExecutionState{}, nil, nil, err
		}
		foundPrepared = true
		break
	}
	var baseData map[string]any
	switch {
	case foundPrepared:
		preparedJSON, err := h.openPII(ctx, generation.id, ref, prepared.Data)
		if err != nil {
			return domain.ExecutionState{}, nil, nil, err
		}
		if err := json.Unmarshal(preparedJSON, &baseData); err != nil {
			return domain.ExecutionState{}, nil, nil, fmt.Errorf("decision-engine: recover prepared context: %w", err)
		}
	case generation.started.RecoveryAfter.IsZero():
		// Legacy DecisionStarted events already contain the authoritative
		// feature/effect-prepared context. Re-reading mutable feature, consent,
		// or provider state would make historical recovery nondeterministic.
		baseData = cloneDecisionInput(rawData)
	default:
		baseData, err = h.injectFeatures(ctx, generation.id, ref, cloneDecisionInput(rawData))
		if err == nil {
			err = h.captureConsent(
				ctx, generation.id, ref, baseData,
				generation.started.DecisionID,
			)
		}
		if err != nil {
			return domain.ExecutionState{}, nil, nil, err
		}
	}
	if !foundPrepared {
		preparedJSON, err := json.Marshal(baseData)
		if err != nil {
			return domain.ExecutionState{}, nil, nil, err
		}
		preparedJSON, err = h.sealPII(ctx, generation.id, ref, preparedJSON)
		if err != nil {
			return domain.ExecutionState{}, nil, nil, err
		}
		if _, err := h.emitEnvelopeUnique(
			ctx, generation.id, events.TypeContextPrepared,
			events.DecisionContextPrepared{DecisionID: generation.started.DecisionID, Data: preparedJSON},
			"decision.context\x00"+generation.started.DecisionID,
		); err != nil && !errors.Is(err, eventlog.ErrConflict) {
			return domain.ExecutionState{}, nil, nil, err
		}
	}
	return domain.StartExecution(version.Graph, baseData), rawData, baseData, nil
}

func (h *DecideHandler) recoveryEffects(
	ctx context.Context,
	generation decisionGeneration,
	ref EntityRef,
	scope string,
) (map[string][]recoveredEffect, error) {
	out := map[string][]recoveredEffect{}
	type location struct {
		effectID string
		index    int
	}
	byAttempt := map[string]location{}
	for _, envelope := range generation.events[generation.boundary:] {
		if envelope.Type != events.TypeEffectRequested {
			continue
		}
		var requested events.DecisionEffectRequested
		if err := json.Unmarshal(envelope.Payload, &requested); err != nil {
			return nil, err
		}
		if requested.Scope != scope {
			continue
		}
		key := fmt.Sprintf("%s\x00%d", requested.EffectID, requested.Attempt)
		out[requested.EffectID] = append(out[requested.EffectID], recoveredEffect{request: requested})
		byAttempt[key] = location{
			effectID: requested.EffectID,
			index:    len(out[requested.EffectID]) - 1,
		}
	}
	for _, envelope := range generation.events[generation.boundary:] {
		switch envelope.Type {
		case events.TypeEffectSucceeded:
			var succeeded events.DecisionEffectSucceeded
			if err := json.Unmarshal(envelope.Payload, &succeeded); err != nil {
				return nil, err
			}
			key := fmt.Sprintf("%s\x00%d", succeeded.EffectID, succeeded.Attempt)
			if location, ok := byAttempt[key]; ok {
				success := succeeded
				out[location.effectID][location.index].success = &success
			}
		case events.TypeEffectFailed:
			var failed events.DecisionEffectFailed
			if err := json.Unmarshal(envelope.Payload, &failed); err != nil {
				return nil, err
			}
			key := fmt.Sprintf("%s\x00%d", failed.EffectID, failed.Attempt)
			if location, ok := byAttempt[key]; ok {
				failure := failed
				out[location.effectID][location.index].failed = &failure
			}
		}
	}
	_ = ctx
	_ = ref
	return out, nil
}

func (h *DecideHandler) driveRecoveredExecution(
	ctx context.Context,
	id identity.Identity,
	graph events.Graph,
	state domain.ExecutionState,
	ref EntityRef,
	governed bool,
	audit *effectAudit,
	evidence map[string][]recoveredEffect,
	legacyPreResolvedEffects bool,
	timeout time.Duration,
) (domain.Run, error) {
	if timeout == 0 {
		timeout = h.evalTimeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
execution:
	for {
		step := domain.AdvanceExecution(ctx, graph, state, nil)
		if step.Run != nil {
			return *step.Run, nil
		}
		request := *step.Effect
		effectID := effectIdentity(audit, request)
		input, err := json.Marshal(request.Input)
		if err != nil {
			return domain.Run{}, err
		}
		sum := sha256.Sum256(input)
		inputHash := hex.EncodeToString(sum[:])
		attempts := evidence[effectID]
		var pending *recoveredEffect
		for i := range attempts {
			item := &attempts[i]
			if item.request.InputHash != inputHash ||
				item.request.NodeID != request.NodeID ||
				item.request.Kind != string(request.Kind) ||
				item.request.Reference != request.Reference {
				return domain.Run{}, fmt.Errorf(
					"decision-engine: recorded effect %q does not match reconstructed request",
					effectID,
				)
			}
			if item.success != nil {
				outputJSON, err := h.openPII(ctx, id, ref, item.success.Output)
				if err != nil {
					return domain.Run{}, err
				}
				var value any
				if err := json.Unmarshal(outputJSON, &value); err != nil {
					return domain.Run{}, fmt.Errorf("decision-engine: recover effect %q output: %w", effectID, err)
				}
				state, err = domain.ResolveEffect(graph, step.State, request, value)
				if err != nil {
					return domain.Run{}, err
				}
				pending = nil
				continue execution
			}
			if item.failed != nil {
				return domain.FailEffect(step.State, request, errors.New(item.failed.Error)), nil
			}
			pending = item
		}
		if len(attempts) == 0 && legacyPreResolvedEffects {
			value, err := legacyEffectValue(request)
			if err != nil {
				return domain.Run{}, err
			}
			state, err = domain.ResolveEffect(graph, step.State, request, value)
			if err != nil {
				return domain.Run{}, err
			}
			continue
		}

		nextAttempt := 1
		delivery, err := h.effectDelivery(ctx, id, request, nil)
		if err != nil {
			return domain.Run{}, err
		}
		if pending != nil {
			nextAttempt = pending.request.Attempt + 1
			delivery = effect.Delivery(pending.request.Delivery)
			if !delivery.Valid() {
				return domain.Run{}, fmt.Errorf(
					"decision-engine: effect %q has invalid recorded delivery %q",
					effectID, pending.request.Delivery,
				)
			}
			if delivery == effect.AtLeastOnce {
				return domain.Run{}, indeterminateEffectError{
					nodeID: request.NodeID, effectID: effectID,
				}
			}
		}
		if err := h.recordEffectRequested(
			ctx, id, *audit, step.State, request, effectID, nextAttempt, delivery,
		); err != nil {
			return domain.Run{}, err
		}
		effectCtx, err := effect.WithRequest(ctx, effect.Request{Key: effectID, Attempt: nextAttempt})
		if err != nil {
			return domain.Run{}, err
		}
		started := h.now()
		value, err := h.performEffect(effectCtx, id, request, ref, governed, governed, nil)
		if err != nil {
			if emitErr := h.recordEffectFailed(
				ctx, id, *audit, request, effectID, nextAttempt, err,
				h.now().Sub(started).Milliseconds(),
			); emitErr != nil {
				return domain.Run{}, emitErr
			}
			return domain.FailEffect(step.State, request, err), nil
		}
		if err := h.recordEffectSucceeded(
			ctx, id, *audit, request, effectID, nextAttempt, value,
			h.now().Sub(started).Milliseconds(),
		); err != nil {
			return domain.Run{}, err
		}
		state, err = domain.ResolveEffect(graph, step.State, request, value)
		if err != nil {
			return domain.Run{}, err
		}
	}
}

func legacyEffectValue(request domain.EffectRequest) (any, error) {
	namespace := string(request.Kind)
	rawBucket, ok := request.Input[namespace]
	if !ok {
		return nil, fmt.Errorf(
			"decision-engine: legacy recovery has no %s namespace for node %q",
			namespace,
			request.NodeID,
		)
	}
	bucket, ok := rawBucket.(map[string]any)
	if !ok {
		return nil, fmt.Errorf(
			"decision-engine: legacy recovery %s namespace for node %q is not an object",
			namespace,
			request.NodeID,
		)
	}
	value, ok := bucket[request.Output]
	if !ok {
		return nil, fmt.Errorf(
			"decision-engine: legacy recovery has no %s.%s value for node %q",
			namespace,
			request.Output,
			request.NodeID,
		)
	}
	return value, nil
}

func (h *DecideHandler) recoveryPolicy(
	ctx context.Context,
	id identity.Identity,
	started events.DecisionStarted,
) (policySelection, error) {
	if !started.PolicySelectionRecorded {
		return policySelection{}, nil
	}
	if started.PolicyID == "" {
		return policySelection{}, nil
	}
	view, version, found, err := policy.ReadVersion(ctx, h.store, id, started.PolicyID, started.PolicyVersion)
	if err != nil {
		return policySelection{}, err
	}
	if !found {
		return policySelection{}, fmt.Errorf(
			"decision-engine: recovery references missing policy %q version %d",
			started.PolicyID, started.PolicyVersion,
		)
	}
	return policySelection{policyID: view.PolicyID, version: version.Version, spec: version.Spec}, nil
}

func (h *DecideHandler) recordRecoveredRun(
	ctx context.Context,
	generation decisionGeneration,
	fv flows.FlowView,
	ref EntityRef,
	rawData, baseData map[string]any,
	run domain.Run,
	selectedPolicy policySelection,
) (uint64, error) {
	decisionID := generation.started.DecisionID
	existingNodes := map[string]bool{}
	for _, envelope := range generation.events[generation.boundary:] {
		if envelope.Type != events.TypeNodeEvaluated {
			continue
		}
		var node events.NodeEvaluated
		if err := json.Unmarshal(envelope.Payload, &node); err != nil {
			return 0, err
		}
		existingNodes[node.NodeID] = true
	}
	var lastSeq uint64
	for _, result := range run.Results {
		if existingNodes[result.NodeID] {
			continue
		}
		output, err := h.sealPII(ctx, generation.id, ref, result.Output)
		if err != nil {
			return 0, err
		}
		envelope, err := h.emitEnvelopeUnique(
			ctx, generation.id, events.TypeNodeEvaluated,
			events.NodeEvaluated{
				DecisionID: decisionID, NodeID: result.NodeID,
				NodeType: result.Type, Output: output,
			},
			decisionNodeClaim(decisionID, generation.generation, result.NodeID),
		)
		if err != nil && !errors.Is(err, eventlog.ErrConflict) {
			return 0, err
		}
		lastSeq = max(lastSeq, envelope.Seq)
	}

	duration := h.now().Sub(generation.startedAt).Milliseconds()
	switch run.Status {
	case domain.StatusFailed:
		envelope, err := h.recordRecoveredTerminal(
			ctx, generation, events.TypeDecisionFailed,
			events.DecisionFailed{
				DecisionID: decisionID, FlowID: generation.started.FlowID,
				Version: generation.started.Version, Variant: generation.started.Variant,
				NodeID: run.FailedNode, Error: run.Err, DurationMS: duration,
			},
		)
		if err != nil {
			return 0, err
		}
		return max(lastSeq, envelope.Seq), nil
	case domain.StatusSuspended:
		stateJSON, err := json.Marshal(run.Suspend)
		if err != nil {
			return 0, err
		}
		stateJSON, err = h.sealPII(ctx, generation.id, ref, stateJSON)
		if err != nil {
			return 0, err
		}
		caseID := h.newID()
		if generation.terminal != nil {
			var suspended events.DecisionSuspended
			if err := json.Unmarshal(generation.terminal.Payload, &suspended); err != nil {
				return 0, fmt.Errorf("decision-engine: decode suspended recovery terminal: %w", err)
			}
			if suspended.CaseID == "" {
				return 0, fmt.Errorf(
					"decision-engine: suspended decision %q has no durable case id",
					decisionID,
				)
			}
			caseID = suspended.CaseID
		}
		envelope, err := h.recordRecoveredTerminal(
			ctx, generation, events.TypeDecisionSuspended,
			events.DecisionSuspended{
				DecisionID: decisionID, FlowID: generation.started.FlowID,
				Version: generation.started.Version, Variant: generation.started.Variant,
				NodeID: run.Suspend.NodeID, ResumeNode: run.Suspend.Resume, CaseID: caseID,
				State: stateJSON, DurationMS: duration,
			},
		)
		if err != nil {
			return 0, err
		}
		preparedJSON, err := json.Marshal(baseData)
		if err != nil {
			return 0, err
		}
		preparedJSON, err = h.sealPII(ctx, generation.id, ref, preparedJSON)
		if err != nil {
			return 0, err
		}
		escalation, err := h.emitEscalations(
			ctx, generation.id, decisionID, generation.generation,
			ref, preparedJSON, run, caseID,
		)
		if err != nil {
			return 0, err
		}
		return max(lastSeq, envelope.Seq, escalation.Seq), nil
	default:
		output, err := json.Marshal(run.Output)
		if err != nil {
			return 0, err
		}
		output, err = h.sealPII(ctx, generation.id, ref, output)
		if err != nil {
			return 0, err
		}
		disposition, policyErr := applyPolicy(selectedPolicy, run.Output)
		if policyErr != nil {
			envelope, err := h.recordRecoveredTerminal(
				ctx,
				generation,
				events.TypeDecisionFailed,
				events.DecisionFailed{
					DecisionID: decisionID, FlowID: generation.started.FlowID,
					Version: generation.started.Version, Variant: generation.started.Variant,
					NodeID: "policy", Error: policyErr.Error(), DurationMS: duration,
				},
			)
			if err != nil {
				return 0, err
			}
			return max(lastSeq, envelope.Seq), nil
		}
		envelope, err := h.recordRecoveredTerminal(
			ctx, generation, events.TypeDecisionCompleted,
			events.DecisionCompleted{
				DecisionID: decisionID, FlowID: generation.started.FlowID,
				Version: generation.started.Version, Variant: generation.started.Variant,
				Output: output, DurationMS: duration,
				Disposition:     string(disposition.disposition),
				DispositionCode: disposition.code, DispositionReason: disposition.reason,
				PolicyID: disposition.policyID, PolicyVersion: disposition.policyVersion,
			},
		)
		if err != nil {
			return 0, err
		}
		var shadow eventlog.Envelope
		if generation.generation == 1 {
			shadowEvidence, err := h.recoveryEffects(ctx, generation, ref, "shadow")
			if err != nil {
				return 0, err
			}
			shadow, err = h.runShadow(
				ctx, generation.id, fv, generation.started.Environment, decisionID,
				generation.started.Version, rawData, baseData, ref,
				domain.Variant(generation.started.Variant),
				generation.started.Environment != string(domain.EnvSandbox),
				selectedPolicy, run, invocationTimeout(generation.started.Control),
				generation.generation, shadowEvidence,
			)
			if err != nil {
				return 0, err
			}
		}
		return max(lastSeq, envelope.Seq, shadow.Seq), nil
	}
}

func (h *DecideHandler) recordRecoveredTerminal(
	ctx context.Context,
	generation decisionGeneration,
	eventType string,
	payload any,
) (eventlog.Envelope, error) {
	if generation.terminal != nil {
		if generation.terminal.Type != eventType {
			return eventlog.Envelope{}, fmt.Errorf(
				"decision-engine: reconstructed decision %q as %s but recorded terminal is %s",
				generation.started.DecisionID,
				eventType,
				generation.terminal.Type,
			)
		}
		return *generation.terminal, nil
	}
	return h.emitEnvelopeUnique(
		ctx,
		generation.id,
		eventType,
		payload,
		decisionTerminalClaim(generation.started.DecisionID, generation.generation),
	)
}

func (h *DecideHandler) abandonDecision(
	ctx context.Context,
	generation decisionGeneration,
	attempt int,
	reason, nodeID string,
) error {
	terminal, err := h.emitEnvelopeUnique(
		ctx, generation.id, events.TypeDecisionAbandoned,
		events.DecisionAbandoned{
			DecisionID: generation.started.DecisionID,
			FlowID:     generation.started.FlowID, Version: generation.started.Version,
			Variant: generation.started.Variant, NodeID: nodeID,
			Error: reason, Attempt: attempt,
		},
		decisionTerminalClaim(generation.started.DecisionID, generation.generation),
	)
	if err != nil {
		return err
	}
	return h.finalizeRecoveredGeneration(ctx, generation, terminal.Seq)
}

func (h *DecideHandler) finalizeRecoveredGeneration(
	ctx context.Context,
	generation decisionGeneration,
	resultSeq uint64,
) error {
	_, err := h.emitEnvelopeUnique(
		ctx, generation.id, events.TypeDecisionFinalized,
		events.DecisionFinalized{
			DecisionID: generation.started.DecisionID,
			ResultSeq:  resultSeq, Generation: generation.generation,
		},
		decisionFinalizedClaim(generation.started.DecisionID, generation.generation),
	)
	if errors.Is(err, eventlog.ErrConflict) {
		return nil
	}
	return err
}

func decisionTerminalClaim(decisionID string, generation int) string {
	return fmt.Sprintf("decision.terminal\x00%s\x00%d", decisionID, generation)
}

func decisionNodeClaim(decisionID string, generation int, nodeID string) string {
	return fmt.Sprintf("decision.node\x00%s\x00%d\x00%s", decisionID, generation, nodeID)
}

func decisionManualReviewClaim(decisionID string, generation int, nodeID string) string {
	return fmt.Sprintf("decision.manual-review\x00%s\x00%d\x00%s", decisionID, generation, nodeID)
}

func decisionFinalizedClaim(decisionID string, generation int) string {
	return fmt.Sprintf("decision.finalized\x00%s\x00%d", decisionID, generation)
}
