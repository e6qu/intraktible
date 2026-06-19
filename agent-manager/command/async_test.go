// SPDX-License-Identifier: AGPL-3.0-or-later

package command_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/agent-manager/command"
	"github.com/e6qu/intraktible/agent-manager/events"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func seedAgent(t *testing.T, st store.Store, id identity.Identity, name string) {
	t.Helper()
	v := agents.AgentView{Org: id.Org, Workspace: id.Workspace, Name: name, Provider: "stub"}
	if err := store.PutDoc(context.Background(), st, agents.CollectionAgents, store.Key(id.Org, id.Workspace, name), v); err != nil {
		t.Fatal(err)
	}
}

// terminalStatus folds the log for a run's recorded outcome (the projection is
// not running in these command-level tests).
func terminalStatus(t *testing.T, log eventlog.Log, runID string) (string, bool) {
	t.Helper()
	evs, err := log.Read(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Type != events.TypeAgentRunRecorded {
			continue
		}
		var p events.AgentRunRecorded
		if err := json.Unmarshal(e.Payload, &p); err == nil && p.RunID == runID {
			return p.Status, true
		}
	}
	return "", false
}

func newAsyncHandler(t *testing.T) (*command.Handler, eventlog.Log, store.Store, identity.Identity, context.CancelFunc) {
	t.Helper()
	log, st := testutil.NewLogStore(t)
	reg := ai.NewRegistry()
	reg.Register(ai.Stub{})
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	h := command.NewHandler(log, st, reg)
	ctx, cancel := context.WithCancel(context.Background())
	h.StartWorkers(ctx, 2)
	return h, log, st, id, cancel
}

func TestStartRunCompletesAsynchronously(t *testing.T) {
	h, log, st, id, cancel := newAsyncHandler(t)
	defer func() { cancel(); h.DrainWorkers() }()
	seedAgent(t, st, id, "echo")

	runID, err := h.StartRun(context.Background(), id, "echo", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if runID == "" {
		t.Fatal("no run id")
	}
	if !testutil.Eventually(t, func() bool {
		s, ok := terminalStatus(t, log, runID)
		return ok && s == "completed"
	}) {
		t.Fatal("async run never reached a recorded completed state")
	}
}

// TestStartRunFullQueueFallbackStaysAsync proves the full-queue fallback runs the
// agent off the request goroutine: StartRun returns promptly (the 202 contract)
// even with no workers draining the queue, and the overflow run still completes.
func TestStartRunFullQueueFallbackStaysAsync(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	reg := ai.NewRegistry()
	reg.Register(ai.Stub{})
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	h := command.NewHandler(log, st, reg)
	// Deliberately start NO workers, so the queue never drains and fills up.
	seedAgent(t, st, id, "echo")

	// Fill the buffered queue (asyncQueueSize = 256) so the next call overflows.
	const queueSize = 256
	for i := 0; i < queueSize; i++ {
		if _, err := h.StartRun(context.Background(), id, "echo", "fill"); err != nil {
			t.Fatal(err)
		}
	}

	type res struct {
		id  string
		err error
	}
	done := make(chan res, 1)
	go func() {
		runID, err := h.StartRun(context.Background(), id, "echo", "overflow")
		done <- res{runID, err}
	}()

	var r res
	select {
	case r = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartRun blocked on the full-queue fallback instead of returning immediately")
	}
	if r.err != nil || r.id == "" {
		t.Fatalf("fallback StartRun: id=%q err=%v", r.id, r.err)
	}
	if !testutil.Eventually(t, func() bool {
		s, ok := terminalStatus(t, log, r.id)
		return ok && s == "completed"
	}) {
		t.Fatal("full-queue fallback run never completed")
	}
	h.DrainWorkers() // waits out the tracked fallback goroutine
}

func TestStartRunRejectsUnknownAgent(t *testing.T) {
	h, _, _, id, cancel := newAsyncHandler(t)
	defer func() { cancel(); h.DrainWorkers() }()
	if _, err := h.StartRun(context.Background(), id, "ghost", "x"); err == nil {
		t.Fatal("an unknown agent should be rejected up front")
	}
}

func TestRecoverRunningReEnqueuesInterruptedRuns(t *testing.T) {
	h, log, st, id, cancel := newAsyncHandler(t)
	defer func() { cancel(); h.DrainWorkers() }()
	seedAgent(t, st, id, "echo")

	// Simulate a crash: an AgentRunStarted with no terminal AgentRunRecorded.
	if _, err := eventlog.AppendJSON(context.Background(), log, id.Org, id.Workspace, id.Actor,
		events.StreamAgents, events.TypeAgentRunStarted, time.Unix(0, 0).UTC(),
		events.AgentRunStarted{RunID: "interrupted", Agent: "echo", Prompt: "resume me"}); err != nil {
		t.Fatal(err)
	}

	n, err := h.RecoverRunning(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("recovered %d runs, want 1", n)
	}
	if !testutil.Eventually(t, func() bool {
		s, ok := terminalStatus(t, log, "interrupted")
		return ok && s == "completed"
	}) {
		t.Fatal("recovered run never completed")
	}
}

func TestRecoverRunningSkipsFinishedRuns(t *testing.T) {
	h, log, st, id, cancel := newAsyncHandler(t)
	defer func() { cancel(); h.DrainWorkers() }()
	seedAgent(t, st, id, "echo")

	// A run that already reached its terminal event must not be re-enqueued.
	for _, ev := range []struct {
		typ string
		p   any
	}{
		{events.TypeAgentRunStarted, events.AgentRunStarted{RunID: "done", Agent: "echo", Prompt: "p"}},
		{events.TypeAgentRunRecorded, events.AgentRunRecorded{RunID: "done", Agent: "echo", Status: "completed"}},
	} {
		if _, err := eventlog.AppendJSON(context.Background(), log, id.Org, id.Workspace, id.Actor,
			events.StreamAgents, ev.typ, time.Unix(0, 0).UTC(), ev.p); err != nil {
			t.Fatal(err)
		}
	}

	n, err := h.RecoverRunning(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("recovered %d runs, want 0 (already finished)", n)
	}
}
