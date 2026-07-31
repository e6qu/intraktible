// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/e6qu/intraktible/platform/effect"
	"github.com/e6qu/intraktible/platform/erasure"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/privacy"
)

const (
	assistLease        = 30 * time.Second
	assistPollInterval = time.Second
	assistCancelPoll   = 500 * time.Millisecond
	assistQueueSize    = 256
	assistWorkerActor  = "agent-governance-worker"
	assistScanHealth   = "queue-scan"
)

type assistJob struct {
	id       identity.Identity
	assistID string
}

type assistResource struct {
	org, workspace, assistID string
}

func (s *Service) StartWorkers(ctx context.Context, count int) error {
	if count <= 0 {
		return nil
	}
	if s.runner == nil || s.contentSealer == nil {
		return errors.New(
			"agent governance: durable assist workers require a runner and content sealer",
		)
	}
	if _, err := s.RecoverAssists(ctx); err != nil {
		return err
	}
	s.workerWG.Add(1)
	go s.pollAssists(ctx)
	for index := 0; index < count; index++ {
		s.workerWG.Add(1)
		go s.assistWorker(ctx)
	}
	return nil
}

func (s *Service) DrainWorkers() {
	s.workerWG.Wait()
}

// Err reports durable worker failures to the process health surface. Failures
// are tracked independently so a healthy queue scan cannot hide an assist whose
// terminal event could not be recorded.
func (s *Service) Err() error {
	s.workerHealth.RLock()
	defer s.workerHealth.RUnlock()
	if len(s.workerErrors) == 0 {
		return nil
	}
	keys := make([]string, 0, len(s.workerErrors))
	for key := range s.workerErrors {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	key := keys[0]
	return fmt.Errorf("%s: %w", key, s.workerErrors[key])
}

func (s *Service) reportWorker(key string, err error) {
	s.workerHealth.Lock()
	defer s.workerHealth.Unlock()
	if err == nil {
		delete(s.workerErrors, key)
		return
	}
	s.workerErrors[key] = err
}

func assistWorkerHealth(assistID string) string {
	return "assist/" + assistID
}

func (s *Service) RecoverAssists(ctx context.Context) (int, error) {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	events, err := s.cmd.log.Read(ctx, s.assistCursor)
	if err != nil {
		return 0, fmt.Errorf("agent governance: scan durable assists: %w", err)
	}
	nextCursor := s.assistCursor
	affected := make(map[assistResource]bool)
	for _, event := range events {
		nextCursor = event.Seq
		if event.Stream != Stream {
			continue
		}
		assistID, relevant, err := durableAssistEventID(event)
		if err != nil {
			return 0, err
		}
		if !relevant {
			continue
		}
		resource := assistResource{event.Org, event.Workspace, assistID}
		affected[resource] = true
		if event.Type == TypeAssistRequested {
			var request AssistRequested
			if err := json.Unmarshal(event.Payload, &request); err != nil {
				return 0, fmt.Errorf(
					"agent governance: decode assist request seq %d: %w", event.Seq, err,
				)
			}
			s.assistOwners[resource] = identity.Identity{
				Org: event.Org, Workspace: event.Workspace, Actor: request.RequestedBy,
			}
		}
	}
	candidates := make(map[assistResource]bool, len(s.activeAssists)+len(affected))
	for resource, active := range s.activeAssists {
		if active {
			candidates[resource] = true
		}
	}
	for resource := range affected {
		candidates[resource] = true
	}
	offered := 0
	for resource := range candidates {
		requester, known := s.assistOwners[resource]
		if !known {
			return offered, fmt.Errorf(
				"agent governance: durable assist %q has no recorded requester",
				resource.assistID,
			)
		}
		work, found, err := s.cmd.assistWorkSnapshot(
			ctx, requester, resource.assistID,
		)
		if err != nil {
			return offered, err
		}
		if !found {
			return offered, fmt.Errorf(
				"agent governance: durable assist %q disappeared", resource.assistID,
			)
		}
		switch work.status {
		case AssistRequestedStatus:
			s.activeAssists[resource] = true
			if s.enqueueAssist(assistJob{id: requester, assistID: work.request.AssistID}) {
				offered++
			}
		case AssistRunningStatus:
			s.activeAssists[resource] = true
			if !s.now().Before(work.leaseUntil) {
				workerID := requester
				workerID.Actor = assistWorkerActor
				if _, err := s.cmd.DeadLetterExpiredAssist(
					ctx, workerID, work.request.AssistID,
				); err != nil && !errors.Is(err, eventlog.ErrConflict) {
					return offered, err
				}
				s.activeAssists[resource] = false
				s.reportWorker(assistWorkerHealth(work.request.AssistID), nil)
			}
		default:
			s.activeAssists[resource] = false
			s.reportWorker(assistWorkerHealth(work.request.AssistID), nil)
		}
	}
	s.assistCursor = nextCursor
	return offered, nil
}

func durableAssistEventID(event eventlog.Envelope) (string, bool, error) {
	switch event.Type {
	case TypeAssistRequested, TypeAssistClaimed, TypeAssistHeartbeat,
		TypeAssistFailed, TypeAssistDeadLettered, TypeAssistRetryRequested,
		TypeAssistCancelRequested, TypeToolApprovalRequested,
		TypeToolApprovalDecided, TypeToolApprovalExpired:
		var payload struct {
			AssistID string `json:"assist_id"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return "", false, fmt.Errorf(
				"agent governance: decode durable assist event %s seq %d: %w",
				event.Type, event.Seq, err,
			)
		}
		if payload.AssistID == "" {
			return "", false, fmt.Errorf(
				"agent governance: durable assist event %s seq %d has no assist id",
				event.Type, event.Seq,
			)
		}
		return payload.AssistID, true, nil
	case TypeAssistCompleted:
		var payload AssistCompleted
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return "", false, fmt.Errorf(
				"agent governance: decode assist completion seq %d: %w", event.Seq, err,
			)
		}
		if payload.Result.AssistID == "" {
			return "", false, fmt.Errorf(
				"agent governance: assist completion seq %d has no assist id", event.Seq,
			)
		}
		return payload.Result.AssistID, true, nil
	default:
		return "", false, nil
	}
}

func (s *Service) pollAssists(ctx context.Context) {
	defer s.workerWG.Done()
	ticker := time.NewTicker(assistPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, err := s.RecoverAssists(ctx)
			if err != nil && ctx.Err() == nil {
				s.reportWorker(assistScanHealth, err)
				slog.Error("agent governance: durable assist scan", "err", err)
				continue
			}
			s.reportWorker(assistScanHealth, nil)
		}
	}
}

func (s *Service) enqueueAssist(job assistJob) bool {
	select {
	case s.assistJobs <- job:
		return true
	default:
		return false
	}
}

func (s *Service) assistWorker(ctx context.Context) {
	defer s.workerWG.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-s.assistJobs:
			s.processAssist(ctx, job)
		}
	}
}

func (s *Service) processAssist(ctx context.Context, job assistJob) {
	healthKey := assistWorkerHealth(job.assistID)
	workerID := job.id
	workerID.Actor = assistWorkerActor
	claim, _, err := s.cmd.ClaimAssist(
		ctx, workerID, job.assistID, s.workerOwner, assistLease,
	)
	if err != nil {
		if !errors.Is(err, eventlog.ErrConflict) {
			status, found, statusErr := s.cmd.AssistStatus(ctx, job.id, job.assistID)
			if statusErr != nil || !found || status == AssistRequestedStatus {
				healthErr := err
				if statusErr != nil {
					healthErr = errors.Join(err, statusErr)
				}
				s.reportWorker(healthKey, healthErr)
				slog.Error(
					"agent governance: claim durable assist",
					"assist_id", job.assistID, "err", err, "status_error", statusErr,
				)
				return
			}
		}
		s.reportWorker(healthKey, nil)
		return
	}
	request, err := s.cmd.AssistSnapshot(ctx, job.id, job.assistID)
	if err != nil {
		s.reportWorker(healthKey, err)
		slog.Error(
			"agent governance: read claimed assist", "assist_id", job.assistID, "err", err,
		)
		return
	}
	input, err := s.openAssistInput(ctx, workerID, request)
	if err != nil {
		s.reportWorker(healthKey, s.failClaimedAssist(ctx, workerID, claim, err))
		return
	}
	release, err := s.cmd.ReleaseSnapshot(
		ctx, workerID, request.TemplateID, request.Release,
	)
	if err != nil {
		s.reportWorker(healthKey, s.failClaimedAssist(ctx, workerID, claim, err))
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	runCtx, err = effect.WithRequest(
		runCtx, effect.Request{Key: "agent-assist:" + request.AssistID, Attempt: claim.Attempt},
	)
	if err != nil {
		cancel()
		s.reportWorker(healthKey, s.failClaimedAssist(ctx, workerID, claim, err))
		return
	}
	heartbeatDone := make(chan error, 1)
	go s.heartbeatAssist(runCtx, cancel, workerID, claim, heartbeatDone)
	approved, hasApproval, err := s.cmd.ApprovedToolCallSnapshot(
		runCtx, workerID, request.AssistID,
	)
	if err != nil {
		cancel()
		<-heartbeatDone
		s.reportWorker(healthKey, s.failClaimedAssist(ctx, workerID, claim, err))
		return
	}
	var result AssistResult
	var runErr error
	if hasApproval {
		result, runErr = s.runner.RunWithToolApproval(
			runCtx, request, release, input.caseView(), approved,
		)
	} else {
		result, runErr = s.runner.Run(runCtx, request, release, input.caseView())
	}
	cancel()
	heartbeatErr := <-heartbeatDone
	if heartbeatErr != nil {
		s.reportWorker(healthKey, heartbeatErr)
		slog.Error(
			"agent governance: lost assist lease", "assist_id", request.AssistID,
			"attempt", claim.Attempt, "err", heartbeatErr,
		)
		return
	}
	if ctx.Err() != nil {
		return
	}
	status, found, statusErr := s.cmd.AssistStatus(ctx, job.id, request.AssistID)
	if statusErr != nil || !found {
		healthErr := statusErr
		if healthErr == nil {
			healthErr = errors.New("agent governance: claimed assist disappeared")
		}
		s.reportWorker(healthKey, healthErr)
		slog.Error(
			"agent governance: read assist after provider",
			"assist_id", request.AssistID, "err", statusErr,
		)
		return
	}
	if status == AssistCancelledStatus {
		s.reportWorker(healthKey, nil)
		return
	}
	if runErr != nil {
		var needed *ToolApprovalNeededError
		if errors.As(runErr, &needed) {
			if _, _, approvalErr := s.cmd.RequestToolApproval(
				ctx, job.id, request.AssistID, *needed, s.now().Add(15*time.Minute),
			); approvalErr != nil {
				s.reportWorker(
					healthKey,
					s.failClaimedAssist(ctx, workerID, claim, approvalErr),
				)
				return
			}
			s.reportWorker(healthKey, nil)
			return
		}
		s.reportWorker(healthKey, s.failClaimedAssist(ctx, workerID, claim, runErr))
		return
	}
	if _, err := s.cmd.CompleteClaimedAssist(
		ctx, workerID, result, claim.Owner, claim.Attempt,
	); err != nil {
		s.reportWorker(healthKey, err)
		slog.Error(
			"agent governance: record claimed assist completion",
			"assist_id", request.AssistID, "attempt", claim.Attempt, "err", err,
		)
		return
	}
	s.reportWorker(healthKey, nil)
}

func (s *Service) heartbeatAssist(
	ctx context.Context,
	cancel context.CancelFunc,
	id identity.Identity,
	claim AssistClaimed,
	done chan<- error,
) {
	heartbeat := time.NewTicker(assistLease / 3)
	defer heartbeat.Stop()
	statusPoll := time.NewTicker(assistCancelPoll)
	defer statusPoll.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case <-statusPoll.C:
			status, found, err := s.cmd.AssistStatus(ctx, id, claim.AssistID)
			if err != nil {
				cancel()
				done <- err
				return
			}
			if !found || status != AssistRunningStatus {
				cancel()
				done <- nil
				return
			}
		case <-heartbeat.C:
			if _, err := s.cmd.HeartbeatAssist(
				ctx, id, claim.AssistID, claim.Owner, claim.Attempt, assistLease,
			); err != nil {
				cancel()
				done <- err
				return
			}
		}
	}
}

func (s *Service) openAssistInput(
	ctx context.Context,
	id identity.Identity,
	request AssistRequested,
) (AssistInput, error) {
	if request.InputSubject == "" || len(request.SealedInput) == 0 {
		return AssistInput{}, errors.New(
			"agent governance: durable assist has no sealed input snapshot",
		)
	}
	plain, err := s.contentSealer.Open(
		ctx, id, request.InputSubject, request.SealedInput,
	)
	if errors.Is(err, erasure.ErrErased) {
		return AssistInput{}, errors.New(
			"agent governance: case subject was erased before assist execution",
		)
	}
	if err != nil {
		return AssistInput{}, fmt.Errorf("agent governance: open assist input: %w", err)
	}
	var input AssistInput
	if err := json.Unmarshal(plain, &input); err != nil {
		return AssistInput{}, fmt.Errorf("agent governance: decode assist input: %w", err)
	}
	if input.CaseID != request.CaseID {
		return AssistInput{}, errors.New(
			"agent governance: sealed assist input has invalid case lineage",
		)
	}
	if _, err := selectCaseEvidence(input.caseView(), request.EvidenceIDs); err != nil {
		return AssistInput{}, err
	}
	return input, nil
}

func (s *Service) failClaimedAssist(
	ctx context.Context,
	id identity.Identity,
	claim AssistClaimed,
	cause error,
) error {
	reason := privacy.RedactTextPII(cause.Error())
	if _, err := s.cmd.FailClaimedAssist(
		ctx, id, claim.AssistID, reason, claim.Owner, claim.Attempt,
	); err != nil {
		slog.Error(
			"agent governance: record claimed assist failure",
			"assist_id", claim.AssistID, "attempt", claim.Attempt,
			"failure", reason, "err", err,
		)
		return fmt.Errorf("record claimed assist failure: %w", err)
	}
	return nil
}
