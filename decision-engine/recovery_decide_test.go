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
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/decision-engine/preapproval"
	"github.com/e6qu/intraktible/platform/effect"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

type failEventOnceLog struct {
	eventlog.Log
	eventType string
	mu        sync.Mutex
	failed    bool
}

type failAfterEventOnceLog struct {
	eventlog.Log
	eventType string
	mu        sync.Mutex
	failed    bool
}

func (l *failAfterEventOnceLog) Append(
	ctx context.Context,
	envelope eventlog.Envelope,
) (eventlog.Envelope, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	stored, err := l.Log.Append(ctx, envelope)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if envelope.Type == l.eventType && !l.failed {
		l.failed = true
		return eventlog.Envelope{}, errors.New("injected lost append acknowledgement")
	}
	return stored, nil
}

func (l *failEventOnceLog) Append(ctx context.Context, envelope eventlog.Envelope) (eventlog.Envelope, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if envelope.Type == l.eventType && !l.failed {
		l.failed = true
		return eventlog.Envelope{}, errors.New("injected append boundary failure")
	}
	return l.Log.Append(ctx, envelope)
}

type recoveryConnector struct {
	mu         sync.Mutex
	delivery   effect.Delivery
	transports int
	logical    int
	seen       map[string]json.RawMessage
}

func (c *recoveryConnector) Fetch(ctx context.Context, _ identity.Identity, _ string, _ json.RawMessage) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transports++
	request, hasRequest := effect.FromContext(ctx)
	if hasRequest && c.delivery == effect.ProviderIdempotent {
		if cached, ok := c.seen[request.Key]; ok {
			return cached, nil
		}
	}
	c.logical++
	output := json.RawMessage(`{"score":80}`)
	if hasRequest && c.delivery == effect.ProviderIdempotent {
		if c.seen == nil {
			c.seen = map[string]json.RawMessage{}
		}
		c.seen[request.Key] = output
	}
	return output, nil
}

func (c *recoveryConnector) EffectDelivery(context.Context, identity.Identity, string) (effect.Delivery, error) {
	return c.delivery, nil
}

func (c *recoveryConnector) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transports, c.logical
}

func TestDecisionRecoveryAcrossEveryPostStartEventBoundary(t *testing.T) {
	for _, eventType := range []string{
		events.TypeContextPrepared,
		events.TypeEffectRequested,
		events.TypeEffectSucceeded,
		events.TypeNodeEvaluated,
		events.TypeDecisionCompleted,
		events.TypeDecisionFinalized,
	} {
		t.Run(eventType, func(t *testing.T) {
			ctx := context.Background()
			base := eventlog.NewMemory()
			id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
			st := store.NewMemory()
			publishFlow(t, ctx, base, st, id, "recover", "Recover", flowtest.ConnectGraph())
			failing := &failEventOnceLog{Log: base, eventType: eventType}
			connector := &recoveryConnector{delivery: effect.ProviderIdempotent}
			handler := command.NewDecideHandler(
				failing, st, command.WithConnectors(connector),
			)

			if result, err := handler.Decide(
				ctx, id, "recover", "sandbox",
				map[string]any{"application_id": "a-1"}, command.EntityRef{},
			); err == nil || result.DecisionID != "" {
				t.Fatalf("injected %s failure returned result=%+v err=%v", eventType, result, err)
			}
			summary, err := handler.RecoverInterrupted(ctx, "worker-a")
			if err != nil {
				t.Fatal(err)
			}
			if summary.Claimed != 1 || summary.Recovered != 1 || summary.Abandoned != 0 {
				t.Fatalf("summary = %+v", summary)
			}

			rebuilt := store.NewMemory()
			if _, err := projection.New(base, rebuilt, history.Projector{}).RebuildTo(ctx, 0); err != nil {
				t.Fatal(err)
			}
			records, err := history.List(ctx, rebuilt, id)
			if err != nil || len(records) != 1 || records[0].Status != "completed" {
				t.Fatalf("records=%+v err=%v", records, err)
			}
			transports, logical := connector.counts()
			if logical != 1 {
				t.Fatalf("provider logical operations = %d, transports=%d; want one", logical, transports)
			}
			if eventType == events.TypeEffectSucceeded && transports != 2 {
				t.Fatalf("indeterminate provider-idempotent call transports = %d, want retry", transports)
			}
			if eventType != events.TypeEffectSucceeded && transports != 1 {
				t.Fatalf("provider transports = %d, want one", transports)
			}
		})
	}
}

func TestDecisionRecoveryReusesLegacyPreResolvedEffects(t *testing.T) {
	ctx := context.Background()
	base := eventlog.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFlow(t, ctx, base, st, id, "legacy-score", "Legacy score", flowtest.PredictGraph())
	flow, found, err := flows.BySlug(ctx, st, id, "legacy-score")
	if err != nil || !found {
		t.Fatalf("flow found=%v err=%v", found, err)
	}

	appendPayload := func(eventType string, payload any) {
		t.Helper()
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := base.Append(ctx, eventlog.Envelope{
			Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
			Stream: events.StreamDecisions, Type: eventType, Payload: encoded,
		}); err != nil {
			t.Fatal(err)
		}
	}
	const decisionID = "legacy-decision"
	appendPayload(events.TypeDecisionStarted, events.DecisionStarted{
		DecisionID: decisionID, FlowID: flow.FlowID, Slug: flow.Slug,
		Version: 1, Environment: "sandbox",
		Data: json.RawMessage(
			`{"fico":700,"predict":{"risk":{"model":"risk","probability":0.62}}}`,
		),
	})
	appendPayload(events.TypeDecisionCompleted, events.DecisionCompleted{
		DecisionID: decisionID, FlowID: flow.FlowID, Version: 1,
		Output: json.RawMessage(`{"tier":"high"}`),
	})

	// No model provider is configured. A historical completed run must be
	// finalized from its recorded eager result, never scored again.
	summary, err := command.NewDecideHandler(base, st).RecoverInterrupted(ctx, "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Claimed != 1 || summary.Recovered != 1 || summary.Abandoned != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	envelopes, err := base.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, envelope := range envelopes {
		counts[envelope.Type]++
	}
	if counts[events.TypeContextPrepared] != 1 ||
		counts[events.TypeDecisionFinalized] != 1 {
		t.Fatalf("legacy migration evidence = %v", counts)
	}
	if counts[events.TypeEffectRequested] != 0 ||
		counts[events.TypeEffectSucceeded] != 0 {
		t.Fatalf("legacy recovery performed a new effect: %v", counts)
	}
}

func TestDecisionRecoveryAbandonsIndeterminateAtLeastOnceEffect(t *testing.T) {
	ctx := context.Background()
	base := eventlog.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFlow(t, ctx, base, st, id, "recover-risk", "Recover risk", flowtest.ConnectGraph())
	failing := &failEventOnceLog{Log: base, eventType: events.TypeEffectSucceeded}
	connector := &recoveryConnector{delivery: effect.AtLeastOnce}
	handler := command.NewDecideHandler(failing, st, command.WithConnectors(connector))

	if _, err := handler.Decide(
		ctx, id, "recover-risk", "sandbox", map[string]any{}, command.EntityRef{},
	); err == nil {
		t.Fatal("injected success-record failure did not interrupt the decision")
	}
	summary, err := handler.RecoverInterrupted(ctx, "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Abandoned != 1 || summary.Recovered != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	transports, logical := connector.counts()
	if transports != 1 || logical != 1 {
		t.Fatalf("at-least-once provider was repeated: transports=%d logical=%d", transports, logical)
	}
	rebuilt := store.NewMemory()
	if _, err := projection.New(base, rebuilt, history.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	records, err := history.List(ctx, rebuilt, id)
	if err != nil || len(records) != 1 || records[0].Status != "abandoned" {
		t.Fatalf("records=%+v err=%v", records, err)
	}
}

func TestConcurrentDecisionRecoveryHasOneActiveClaimAndLogicalEffect(t *testing.T) {
	ctx := context.Background()
	base := eventlog.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFlow(t, ctx, base, st, id, "recover-race", "Recover race", flowtest.ConnectGraph())
	failing := &failEventOnceLog{Log: base, eventType: events.TypeEffectRequested}
	connector := &recoveryConnector{delivery: effect.ProviderIdempotent}
	initial := command.NewDecideHandler(failing, st, command.WithConnectors(connector))
	if _, err := initial.Decide(
		ctx, id, "recover-race", "sandbox", map[string]any{}, command.EntityRef{},
	); err == nil {
		t.Fatal("injected request-record failure did not interrupt the decision")
	}

	a := command.NewDecideHandler(base, st, command.WithConnectors(connector))
	b := command.NewDecideHandler(base, st, command.WithConnectors(connector))
	start := make(chan struct{})
	results := make(chan command.RecoverySummary, 2)
	errs := make(chan error, 2)
	for worker, handler := range map[string]*command.DecideHandler{"worker-a": a, "worker-b": b} {
		go func() {
			<-start
			summary, err := handler.RecoverInterrupted(ctx, worker)
			results <- summary
			errs <- err
		}()
	}
	close(start)
	first, second := <-results, <-results
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if first.Claimed+second.Claimed != 1 || first.Recovered+second.Recovered != 1 {
		t.Fatalf("summaries = %+v %+v", first, second)
	}
	transports, logical := connector.counts()
	if transports != 1 || logical != 1 {
		t.Fatalf("provider calls: transports=%d logical=%d", transports, logical)
	}

	envelopes, err := base.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	claims := 0
	for _, envelope := range envelopes {
		if envelope.Type == events.TypeRecoveryClaimed {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("recovery claims = %d, want one", claims)
	}
}

func TestDecisionRecoveryIsIdempotentAfterLostAppendAcknowledgement(t *testing.T) {
	for _, eventType := range []string{
		events.TypeContextPrepared,
		events.TypeEffectRequested,
		events.TypeEffectSucceeded,
		events.TypeNodeEvaluated,
		events.TypeDecisionCompleted,
		events.TypeDecisionFinalized,
	} {
		t.Run(eventType, func(t *testing.T) {
			ctx := context.Background()
			base := eventlog.NewMemory()
			id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
			st := store.NewMemory()
			publishFlow(t, ctx, base, st, id, "recover-after", "Recover after", flowtest.ConnectGraph())
			failing := &failAfterEventOnceLog{Log: base, eventType: eventType}
			connector := &recoveryConnector{delivery: effect.ProviderIdempotent}
			handler := command.NewDecideHandler(
				failing, st, command.WithConnectors(connector),
			)

			if _, err := handler.Decide(
				ctx, id, "recover-after", "sandbox",
				map[string]any{"application_id": "a-1"}, command.EntityRef{},
			); err == nil {
				t.Fatalf("lost acknowledgement for %s did not interrupt caller", eventType)
			}
			summary, err := handler.RecoverInterrupted(ctx, "worker-a")
			if err != nil {
				t.Fatal(err)
			}
			if eventType == events.TypeDecisionFinalized {
				if summary.Claimed != 0 {
					t.Fatalf("already-finalized summary = %+v", summary)
				}
			} else if summary.Claimed != 1 || summary.Recovered != 1 {
				t.Fatalf("summary = %+v", summary)
			}
			envelopes, err := base.Read(ctx, 0)
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, envelope := range envelopes {
				counts[envelope.Type]++
			}
			for _, uniqueType := range []string{
				events.TypeContextPrepared,
				events.TypeEffectSucceeded,
				events.TypeDecisionCompleted,
				events.TypeDecisionFinalized,
			} {
				if counts[uniqueType] != 1 {
					t.Fatalf("%s count = %d, want one; all=%v", uniqueType, counts[uniqueType], counts)
				}
			}
			wantRequests := 1
			if eventType == events.TypeEffectRequested {
				// Attempt 1 is durably pending: recovery records attempt 2 before
				// making the first transport call with the same logical effect key.
				wantRequests = 2
			}
			if counts[events.TypeEffectRequested] != wantRequests {
				t.Fatalf(
					"effect requests = %d, want %d; all=%v",
					counts[events.TypeEffectRequested], wantRequests, counts,
				)
			}
			transports, logical := connector.counts()
			if transports != 1 || logical != 1 {
				t.Fatalf("provider calls: transports=%d logical=%d", transports, logical)
			}
		})
	}
}

type blockedRecoveryConnector struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func (c *blockedRecoveryConnector) Fetch(
	ctx context.Context,
	_ identity.Identity,
	_ string,
	_ json.RawMessage,
) (json.RawMessage, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	select {
	case c.started <- struct{}{}:
	default:
	}
	select {
	case <-c.release:
		return json.RawMessage(`{"score":80}`), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (*blockedRecoveryConnector) EffectDelivery(
	context.Context,
	identity.Identity,
	string,
) (effect.Delivery, error) {
	return effect.ProviderIdempotent, nil
}

func TestDecisionRecoveryDoesNotClaimActiveSynchronousExecution(t *testing.T) {
	ctx := context.Background()
	base := eventlog.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFlow(t, ctx, base, st, id, "active", "Active", flowtest.ConnectGraph())
	connector := &blockedRecoveryConnector{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	handler := command.NewDecideHandler(base, st, command.WithConnectors(connector))
	done := make(chan error, 1)
	go func() {
		_, err := handler.Decide(
			ctx, id, "active", "sandbox", map[string]any{}, command.EntityRef{},
		)
		done <- err
	}()
	<-connector.started
	recovery := command.NewDecideHandler(base, st, command.WithConnectors(connector))
	summary, err := recovery.RecoverInterrupted(ctx, "worker-b")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Claimed != 0 {
		t.Fatalf("active execution was claimed: %+v", summary)
	}
	close(connector.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	connector.mu.Lock()
	defer connector.mu.Unlock()
	if connector.calls != 1 {
		t.Fatalf("provider calls = %d, want one", connector.calls)
	}
}

func TestDecisionRecoveryRepairsSuspendedManualReviewSideEffect(t *testing.T) {
	ctx := context.Background()
	base := eventlog.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFlow(t, ctx, base, st, id, "suspend-recover", "Suspend recover", suspendGraph())
	failing := &failEventOnceLog{Log: base, eventType: events.TypeManualReviewRequested}
	handler := command.NewDecideHandler(failing, st)
	if _, err := handler.Decide(
		ctx, id, "suspend-recover", "sandbox", map[string]any{}, command.EntityRef{},
	); err == nil {
		t.Fatal("manual-review append failure did not interrupt the decision")
	}
	summary, err := handler.RecoverInterrupted(ctx, "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Recovered != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	envelopes, err := base.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var suspended events.DecisionSuspended
	var review events.ManualReviewRequested
	suspensions, reviews := 0, 0
	for _, envelope := range envelopes {
		switch envelope.Type {
		case events.TypeDecisionSuspended:
			suspensions++
			if err := json.Unmarshal(envelope.Payload, &suspended); err != nil {
				t.Fatal(err)
			}
		case events.TypeManualReviewRequested:
			reviews++
			if err := json.Unmarshal(envelope.Payload, &review); err != nil {
				t.Fatal(err)
			}
		}
	}
	if suspensions != 1 || reviews != 1 || suspended.CaseID == "" ||
		review.CaseID != suspended.CaseID {
		t.Fatalf(
			"suspensions=%d reviews=%d suspended_case=%q review_case=%q",
			suspensions, reviews, suspended.CaseID, review.CaseID,
		)
	}
}

func TestDecisionRecoveryRepairsPreApprovalFastPath(t *testing.T) {
	ctx := context.Background()
	base := eventlog.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	started := events.DecisionStarted{
		DecisionID: "decision-pa", FlowID: "flow-pa", Slug: "credit",
		Version: 1, Environment: "sandbox",
		EntityType: "customer", EntityID: "customer-1",
		Data:          json.RawMessage(`{"amount":100}`),
		PreApprovalID: "pa-1", PreApprovalDisposition: "approve",
		PreApprovalTerms: json.RawMessage(`{"limit":5000}`),
		RecoveryAfter:    time.Now().Add(time.Hour),
	}
	appendPayload := func(eventType string, payload any) {
		t.Helper()
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := base.Append(ctx, eventlog.Envelope{
			Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
			Stream: events.StreamDecisions, Type: eventType, Payload: encoded,
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendPayload(events.TypeDecisionStarted, started)
	appendPayload(events.TypeExecutionInterrupted, events.DecisionExecutionInterrupted{
		DecisionID: started.DecisionID, Generation: 1, Error: "lost acknowledgement",
	})
	handler := command.NewDecideHandler(base, store.NewMemory())
	summary, err := handler.RecoverInterrupted(ctx, "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Recovered != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	envelopes, err := base.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, envelope := range envelopes {
		counts[envelope.Type]++
	}
	for _, eventType := range []string{
		events.TypeDecisionCompleted,
		preapproval.TypeHonored,
		events.TypeDecisionFinalized,
	} {
		if counts[eventType] != 1 {
			t.Fatalf("%s count = %d, want one; all=%v", eventType, counts[eventType], counts)
		}
	}
	second, err := handler.RecoverInterrupted(ctx, "worker-b")
	if err != nil || second.Claimed != 0 {
		t.Fatalf("second recovery = %+v, %v", second, err)
	}
}
