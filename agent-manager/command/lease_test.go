// SPDX-License-Identifier: AGPL-3.0-or-later

package command_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/e6qu/intraktible/agent-manager/command"
	"github.com/e6qu/intraktible/agent-manager/domain"
	"github.com/e6qu/intraktible/agent-manager/events"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

type controlledProvider struct {
	mu        sync.Mutex
	calls     int
	started   chan struct{}
	release   chan struct{}
	failFirst bool
}

func (p *controlledProvider) Name() string { return "controlled" }

func (p *controlledProvider) Complete(ctx context.Context, _ ai.Request) (ai.Response, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if p.started != nil {
		select {
		case p.started <- struct{}{}:
		default:
		}
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return ai.Response{}, ctx.Err()
		}
	}
	if p.failFirst && call == 1 {
		return ai.Response{}, errors.New("injected provider failure")
	}
	return ai.Response{Text: "ok", Model: "controlled"}, nil
}

func (p *controlledProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func workerHandler(
	t *testing.T,
	log eventlog.Log,
	st store.Store,
	provider ai.Provider,
	id identity.Identity,
) (*command.Handler, context.CancelFunc) {
	t.Helper()
	registry := ai.NewRegistry()
	registry.Register(provider)
	seedAgentProvider(t, st, id, "echo", provider.Name())
	handler := command.NewHandler(log, st, registry)
	ctx, cancel := context.WithCancel(context.Background())
	handler.StartWorkers(ctx, 2)
	return handler, func() {
		cancel()
		handler.DrainWorkers()
	}
}

func countRunEvents(t *testing.T, log eventlog.Log, runID, eventType string) int {
	t.Helper()
	envelopes, err := log.Read(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, envelope := range envelopes {
		if envelope.Type != eventType {
			continue
		}
		id, relevant, err := runIDFromEvent(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if relevant && id == runID {
			count++
		}
	}
	return count
}

func runIDFromEvent(envelope eventlog.Envelope) (string, bool, error) {
	switch envelope.Type {
	case events.TypeAgentRunStarted,
		events.TypeAgentRunClaimed,
		events.TypeAgentRunHeartbeat,
		events.TypeAgentRunRetryRequested,
		events.TypeAgentRunCancelRequested,
		events.TypeAgentRunDeadLettered,
		events.TypeAgentRunRecorded:
		var payload struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			return "", true, err
		}
		return payload.RunID, true, nil
	default:
		return "", false, nil
	}
}

func TestAsyncRunClaimIsExclusiveAcrossWorkerReplicas(t *testing.T) {
	log := eventlog.NewMemory()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "operator"}
	provider := &controlledProvider{
		started: make(chan struct{}, 4),
		release: make(chan struct{}),
	}
	first, stopFirst := workerHandler(t, log, st, provider, id)
	defer stopFirst()
	second, stopSecond := workerHandler(t, log, st, provider, id)
	defer stopSecond()

	runID, err := first.StartRun(context.Background(), id, "echo", "hello")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider never started")
	}
	for range 5 {
		_, _ = first.RecoverRunning(context.Background())
		_, _ = second.RecoverRunning(context.Background())
	}
	time.Sleep(50 * time.Millisecond)
	if calls := provider.count(); calls != 1 {
		t.Fatalf("provider calls while leased = %d, want one", calls)
	}
	if claims := countRunEvents(t, log, runID, events.TypeAgentRunClaimed); claims != 1 {
		t.Fatalf("claims = %d, want one", claims)
	}
	close(provider.release)
	if !testutil.Eventually(t, func() bool {
		status, ok := terminalStatus(t, log, runID)
		return ok && status == string(domain.RunCompleted)
	}) {
		t.Fatal("run did not complete")
	}
	if terminals := countRunEvents(t, log, runID, events.TypeAgentRunRecorded); terminals != 1 {
		t.Fatalf("terminal outcomes = %d, want one", terminals)
	}
}

func TestAsyncRunCancellationTimeoutAndExplicitRetry(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		log := eventlog.NewMemory()
		st := store.NewMemory()
		id := identity.Identity{Org: "demo", Workspace: "main", Actor: "operator"}
		provider := &controlledProvider{
			started: make(chan struct{}, 1),
			release: make(chan struct{}),
		}
		handler, stop := workerHandler(t, log, st, provider, id)
		defer stop()
		runID, err := handler.StartRun(context.Background(), id, "echo", "wait")
		if err != nil {
			t.Fatal(err)
		}
		<-provider.started
		if _, err := handler.CancelRun(context.Background(), id, runID); err != nil {
			t.Fatal(err)
		}
		if !testutil.Eventually(t, func() bool {
			status, ok := terminalStatus(t, log, runID)
			return ok && status == string(domain.RunCancelled)
		}) {
			t.Fatal("run did not cancel")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		log := eventlog.NewMemory()
		st := store.NewMemory()
		id := identity.Identity{Org: "demo", Workspace: "main", Actor: "operator"}
		provider := &controlledProvider{release: make(chan struct{})}
		handler, stop := workerHandler(t, log, st, provider, id)
		defer stop()
		runID, _, err := handler.StartRunWithOptions(
			context.Background(), id, "echo", "wait",
			command.AsyncRunOptions{Timeout: 10 * time.Millisecond},
		)
		if err != nil {
			t.Fatal(err)
		}
		if !testutil.Eventually(t, func() bool {
			status, ok := terminalStatus(t, log, runID)
			return ok && status == string(domain.RunTimedOut)
		}) {
			t.Fatal("run did not time out")
		}
	})

	t.Run("retry", func(t *testing.T) {
		log := eventlog.NewMemory()
		st := store.NewMemory()
		id := identity.Identity{Org: "demo", Workspace: "main", Actor: "operator"}
		provider := &controlledProvider{failFirst: true}
		handler, stop := workerHandler(t, log, st, provider, id)
		defer stop()
		runID, err := handler.StartRun(context.Background(), id, "echo", "retry")
		if err != nil {
			t.Fatal(err)
		}
		if !testutil.Eventually(t, func() bool {
			status, ok := terminalStatus(t, log, runID)
			return ok && status == string(domain.RunFailed)
		}) {
			t.Fatal("first attempt did not fail")
		}
		if _, err := handler.RetryRun(
			context.Background(), id, runID, "provider recovered", false,
		); err != nil {
			t.Fatal(err)
		}
		if !testutil.Eventually(t, func() bool {
			envelopes, err := log.Read(context.Background(), 0)
			if err != nil {
				return false
			}
			for i := len(envelopes) - 1; i >= 0; i-- {
				if envelopes[i].Type != events.TypeAgentRunRecorded {
					continue
				}
				id, relevant, _ := runIDFromEvent(envelopes[i])
				if relevant && id == runID {
					var recorded events.AgentRunRecorded
					_ = json.Unmarshal(envelopes[i].Payload, &recorded)
					return recorded.Status == string(domain.RunCompleted) && recorded.Attempt == 2
				}
			}
			return false
		}) {
			t.Fatal("retry did not complete on attempt 2")
		}
		if provider.count() != 2 {
			t.Fatalf("provider calls = %d, want two attempts", provider.count())
		}
	})
}

func TestConcurrentAsyncSubmissionIdempotency(t *testing.T) {
	log := eventlog.NewMemory()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "operator"}
	registry := ai.NewRegistry()
	registry.Register(ai.Stub{})
	seedAgent(t, st, id, "echo")
	handler := command.NewHandler(log, st, registry, command.WithEnqueueOnStart(false))
	options := command.AsyncRunOptions{IdempotencyKey: "run-42"}
	start := make(chan struct{})
	ids := make(chan string, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			runID, _, err := handler.StartRunWithOptions(
				context.Background(), id, "echo", "same", options,
			)
			ids <- runID
			errs <- err
		}()
	}
	close(start)
	a, b := <-ids, <-ids
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if a == "" || a != b {
		t.Fatalf("run ids = %q %q", a, b)
	}
	if starts := countRunEvents(t, log, a, events.TypeAgentRunStarted); starts != 1 {
		t.Fatalf("starts = %d, want one", starts)
	}
}
