// SPDX-License-Identifier: AGPL-3.0-or-later

package population

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/platform/entity"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/metrics"
	"github.com/e6qu/intraktible/platform/store"
)

// StartWorkers launches a bounded cross-tenant worker pool. Work discovery is
// projection-backed; item claims remain event-log unique across replicas.
func (h *Handler) StartWorkers(ctx context.Context, count int) {
	if count < 1 {
		panic("population: worker count must be positive")
	}
	for i := 0; i < count; i++ {
		owner := "population-" + h.newID()
		h.workers.Add(1)
		go func() {
			defer h.workers.Done()
			h.runWorker(ctx, owner)
		}()
	}
}

// DrainWorkers waits for all workers after their context is cancelled.
func (h *Handler) DrainWorkers() { h.workers.Wait() }

func (h *Handler) runWorker(ctx context.Context, owner string) {
	ticker := time.NewTicker(workerPoll)
	defer ticker.Stop()
	for {
		worked, err := h.Tick(ctx, owner)
		if err != nil && !errors.Is(err, context.Canceled) {
			metrics.RecordSchedulerTick("population_worker", "error")
			slog.Error("population worker tick failed", "worker", owner, "error", err)
		}
		if worked {
			metrics.RecordSchedulerTick("population_worker", "ok")
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick claims and processes at most one logical item, or closes one job. It is
// exported for deterministic worker/recovery tests.
func (h *Handler) Tick(ctx context.Context, owner string) (bool, error) {
	records, err := h.store.List(ctx, Collection, "")
	if err != nil {
		return false, err
	}
	for _, record := range records {
		var view View
		if err := json.Unmarshal(record.Doc, &view); err != nil {
			return false, fmt.Errorf("population: decode job projection %q: %w", record.Key, err)
		}
		if view.State == StateCancelling {
			if view.Running == 0 {
				return true, h.finishCancelled(ctx, view)
			}
			continue
		}
		if view.State != StateQueued && view.State != StateRunning {
			continue
		}
		index, attempt, found := claimable(view, h.now())
		if !found {
			if view.Running == 0 {
				return true, h.finishCompleted(ctx, view)
			}
			continue
		}
		// An expired claim still contributes to Running. It must be allowed to
		// consume its own slot, otherwise a job at its concurrency limit can never
		// recover after a worker dies. Pending/retry items still obey the limit.
		if view.Running >= view.Concurrency && view.Items[index].State != "claimed" {
			continue
		}
		claimed, err := h.claim(ctx, view, index, attempt, owner)
		if err != nil {
			if errors.Is(err, eventlog.ErrConflict) {
				continue
			}
			return false, err
		}
		if !claimed {
			continue
		}
		return true, h.process(ctx, view, index, attempt, owner)
	}
	return false, nil
}

func claimable(view View, now time.Time) (index, attempt int, found bool) {
	for _, item := range view.Items {
		switch {
		case item.State == "pending":
			if item.Attempt+1 <= view.MaxAttempts {
				return item.Index, item.Attempt + 1, true
			}
		case item.State == "failed" && item.Retryable && item.Attempt < view.MaxAttempts:
			return item.Index, item.Attempt + 1, true
		case item.State == "claimed" && !item.LeaseUntil.After(now) &&
			item.Attempt < view.MaxAttempts+maxWorkerRecoveries:
			return item.Index, item.Attempt + 1, true
		}
	}
	return 0, 0, false
}

func (h *Handler) claim(ctx context.Context, view View, index, attempt int, owner string) (bool, error) {
	workerID, err := identity.New(view.Org, view.Workspace, "population-worker:"+owner)
	if err != nil {
		return false, err
	}
	_, err = eventlog.AppendJSONUnique(
		ctx, h.log, workerID.Org, workerID.Workspace, workerID.Actor,
		Stream, TypeItemClaimed, h.now(),
		ItemClaimed{
			JobID: view.JobID, Index: index, Attempt: attempt,
			Worker: owner, LeaseUntil: h.now().Add(itemLease),
		},
		itemAttemptClaim(view.JobID, index, attempt),
	)
	return err == nil, err
}

func (h *Handler) process(ctx context.Context, view View, index, attempt int, owner string) error {
	if index < 0 || index >= len(view.Manifest.Items) {
		return fmt.Errorf("population: manifest item %d is out of range", index)
	}
	item := view.Manifest.Items[index]
	workerID, err := identity.New(view.Org, view.Workspace, "population-worker:"+owner)
	if err != nil {
		return err
	}
	invocation := command.Invocation{
		Data:   item.Input.Data,
		Entity: entity.Ref{Type: entity.Type(item.Input.EntityType), ID: entity.ID(item.Input.EntityID)},
	}
	if view.Manifest.Kind == KindDecision {
		invocation.IdempotencyKey = "population:" + view.JobID + ":" + strconv.Itoa(index)
		invocation.BusinessReference = item.Input.BusinessReference
		invocation.CorrelationID = item.Input.CorrelationID
		invocation.Metadata = item.Input.Metadata
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatErrors := make(chan error, 1)
	go h.heartbeat(runCtx, workerID, view.JobID, index, attempt, owner, heartbeatErrors)

	var result command.DecideResult
	if view.Manifest.Kind == KindBacktest {
		result, err = h.decide.PreviewPinned(
			ctx, workerID, view.Manifest.Slug, view.Manifest.Environment,
			invocation, item.Version, item.Assignment,
		)
	} else {
		result, err = h.decide.DecidePinned(
			ctx, workerID, view.Manifest.Slug, view.Manifest.Environment,
			invocation, item.Version, item.Assignment,
		)
	}
	cancel()
	if heartbeatErr := <-heartbeatErrors; heartbeatErr != nil && err == nil {
		err = heartbeatErr
	}
	if err != nil {
		retryable := !errors.Is(err, command.ErrBadRequest) && !errors.Is(err, command.ErrNotFound)
		_, appendErr := eventlog.AppendJSONUnique(
			context.WithoutCancel(ctx), h.log, workerID.Org, workerID.Workspace, workerID.Actor,
			Stream, TypeItemFailed, h.now(),
			ItemFailed{
				JobID: view.JobID, Index: index, Attempt: attempt,
				Error: err.Error(), Retryable: retryable,
			},
			itemOutcomeClaim(view.JobID, index, attempt),
		)
		return appendErr
	}
	_, err = eventlog.AppendJSONUnique(
		context.WithoutCancel(ctx), h.log, workerID.Org, workerID.Workspace, workerID.Actor,
		Stream, TypeItemSucceeded, h.now(),
		ItemSucceeded{
			JobID: view.JobID, Index: index, Attempt: attempt,
			DecisionID: result.DecisionID, Status: string(result.Status), Output: result.Output,
			Disposition: string(result.Disposition), Error: result.Error,
		},
		itemOutcomeClaim(view.JobID, index, attempt),
	)
	return err
}

func (h *Handler) heartbeat(
	ctx context.Context,
	id identity.Identity,
	jobID string,
	index, attempt int,
	owner string,
	errorsOut chan<- error,
) {
	defer close(errorsOut)
	ticker := time.NewTicker(itemLease / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, err := eventlog.AppendJSON(
				ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeItemHeartbeat, h.now(),
				ItemHeartbeat{
					JobID: jobID, Index: index, Attempt: attempt,
					Worker: owner, LeaseUntil: h.now().Add(itemLease),
				},
			)
			if err != nil {
				select {
				case errorsOut <- err:
				default:
				}
				return
			}
		}
	}
}

func (h *Handler) finishCompleted(ctx context.Context, view View) error {
	id, err := identity.New(view.Org, view.Workspace, "population-worker")
	if err != nil {
		return err
	}
	_, err = eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeCompleted, h.now(),
		Completed{JobID: view.JobID, Succeeded: view.Succeeded, Failed: view.Failed},
		"population.final\x00"+view.JobID+"\x00"+view.StateToken,
	)
	if errors.Is(err, eventlog.ErrConflict) {
		return nil
	}
	return err
}

func (h *Handler) finishCancelled(ctx context.Context, view View) error {
	id, err := identity.New(view.Org, view.Workspace, "population-worker")
	if err != nil {
		return err
	}
	_, err = eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeCancelled, h.now(),
		Transition{JobID: view.JobID},
		"population.final\x00"+view.JobID+"\x00"+view.StateToken,
	)
	if errors.Is(err, eventlog.ErrConflict) {
		return nil
	}
	return err
}

func itemAttemptClaim(jobID string, index, attempt int) string {
	return "population.item.claim\x00" + jobID + "\x00" +
		strconv.Itoa(index) + "\x00" + strconv.Itoa(attempt)
}

func itemOutcomeClaim(jobID string, index, attempt int) string {
	return "population.item.outcome\x00" + jobID + "\x00" +
		strconv.Itoa(index) + "\x00" + strconv.Itoa(attempt)
}

// Scheduler expires retained result manifests. The append-only input and audit
// facts remain; projected output bodies become inaccessible.
type Scheduler struct {
	log   eventlog.Log
	store store.Store
	now   func() time.Time
}

func NewScheduler(log eventlog.Log, st store.Store) *Scheduler {
	return &Scheduler{log: log, store: st, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Scheduler) WithNow(now func() time.Time) *Scheduler {
	s.now = now
	return s
}

func (s *Scheduler) Tick(ctx context.Context) error {
	records, err := s.store.List(ctx, Collection, "")
	if err != nil {
		return err
	}
	for _, record := range records {
		var view View
		if err := json.Unmarshal(record.Doc, &view); err != nil {
			return err
		}
		if !view.State.terminal() || view.State == StateExpired ||
			view.ExpiresAt.IsZero() || s.now().Before(view.ExpiresAt) {
			continue
		}
		id, err := identity.New(view.Org, view.Workspace, "population-retention")
		if err != nil {
			return err
		}
		_, err = eventlog.AppendJSONUnique(
			ctx, s.log, id.Org, id.Workspace, id.Actor, Stream, TypeExpired, s.now(),
			Transition{JobID: view.JobID, Reason: "result retention elapsed"},
			"population.expire\x00"+view.JobID,
		)
		if err != nil && !errors.Is(err, eventlog.ErrConflict) {
			return err
		}
	}
	return nil
}

func (s *Scheduler) Run(ctx context.Context, interval time.Duration, report func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := s.Tick(ctx)
			if err != nil {
				metrics.RecordSchedulerTick("population_retention", "error")
			} else {
				metrics.RecordSchedulerTick("population_retention", "ok")
			}
			report(err)
		}
	}
}
