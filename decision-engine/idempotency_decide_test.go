// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

type countedConnector struct {
	mu    sync.Mutex
	calls int
	delay time.Duration
}

func (c *countedConnector) Fetch(
	ctx context.Context,
	_ identity.Identity,
	_ string,
	_ json.RawMessage,
) (json.RawMessage, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	if c.delay > 0 {
		timer := time.NewTimer(c.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return json.RawMessage(`{"score":80}`), nil
}

func (c *countedConnector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestDecideIdempotencyReturnsOriginalResultAndPersistsTracking(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFlow(t, ctx, log, st, id, "idempotent", "Idempotent", flowtest.ConnectGraph())
	connector := &countedConnector{}
	handler := command.NewDecideHandler(log, st, command.WithConnectors(connector))
	invocation := command.Invocation{
		Data:              map[string]any{"application_id": "app-7"},
		IdempotencyKey:    "submit-app-7",
		BusinessReference: "application/app-7",
		CorrelationID:     "trace-42",
		Metadata:          json.RawMessage(`{"channel":"partner-api"}`),
		Control:           events.ExecutionControl{TimeoutMS: 5_000},
	}

	first, err := handler.DecideWithInvocation(ctx, id, "idempotent", "sandbox", invocation)
	if err != nil {
		t.Fatal(err)
	}
	second, err := handler.DecideWithInvocation(ctx, id, "idempotent", "sandbox", invocation)
	if err != nil {
		t.Fatal(err)
	}
	if first.DecisionID != second.DecisionID || first.EventSeq != second.EventSeq ||
		first.Status != domain.StatusCompleted || second.Output["tier"] != "high" {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if connector.count() != 1 {
		t.Fatalf("connector calls = %d, want 1", connector.count())
	}

	historyStore := store.NewMemory()
	if _, err := projection.New(log, historyStore, history.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	record, ok, err := history.Read(ctx, historyStore, id, first.DecisionID)
	if err != nil || !ok {
		t.Fatalf("history read: ok=%v err=%v", ok, err)
	}
	if record.BusinessReference != "application/app-7" || record.CorrelationID != "trace-42" ||
		string(record.Metadata) != `{"channel":"partner-api"}` || record.Control.TimeoutMS != 5_000 {
		t.Fatalf("tracking record = %+v", record)
	}
	if record.IdempotencyKeyHash == "" || record.RequestHash == "" {
		t.Fatalf("idempotency evidence missing: %+v", record)
	}

	conflict := invocation
	conflict.Data = map[string]any{"application_id": "different"}
	if _, err := handler.DecideWithInvocation(ctx, id, "idempotent", "sandbox", conflict); !errors.Is(err, command.ErrBadRequest) {
		t.Fatalf("conflicting key reuse error = %v, want ErrBadRequest", err)
	}
}

func TestConcurrentIdempotentDecideExecutesOneLogicalRun(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFlow(t, ctx, log, st, id, "concurrent-idempotent", "Concurrent", flowtest.ConnectGraph())
	connector := &countedConnector{delay: 50 * time.Millisecond}
	handler := command.NewDecideHandler(log, st, command.WithConnectors(connector))
	invocation := command.Invocation{
		Data:           map[string]any{"application_id": "app-9"},
		IdempotencyKey: "submit-app-9",
	}

	type outcome struct {
		result command.DecideResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			result, err := handler.DecideWithInvocation(
				ctx, id, "concurrent-idempotent", "sandbox", invocation,
			)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	a, b := <-outcomes, <-outcomes
	if a.err != nil || b.err != nil {
		t.Fatalf("errors: %v, %v", a.err, b.err)
	}
	if a.result.DecisionID == "" || a.result.DecisionID != b.result.DecisionID ||
		a.result.EventSeq != b.result.EventSeq {
		t.Fatalf("results: %+v %+v", a.result, b.result)
	}
	if connector.count() != 1 {
		t.Fatalf("connector calls = %d, want 1", connector.count())
	}
}
