// SPDX-License-Identifier: AGPL-3.0-or-later

package command_test

import (
	"context"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/agent-manager/command"
	agentdomain "github.com/e6qu/intraktible/agent-manager/domain"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

// fakeStream is an ai.StreamingProvider that emits the response in fixed deltas —
// the real token-streaming branch the Stub (non-streaming) fallback can't exercise.
type fakeStream struct{}

func (fakeStream) Name() string { return "fakestream" }
func (fakeStream) Complete(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{Text: "abc"}, nil
}
func (fakeStream) Stream(_ context.Context, _ ai.Request, onChunk ai.StreamHandler) (ai.Response, error) {
	for _, p := range []string{"a", "b", "c"} {
		onChunk(ai.Chunk{Text: p})
	}
	return ai.Response{Text: "abc"}, nil
}

func seedAgentProvider(t *testing.T, st store.Store, id identity.Identity, name, provider string) {
	t.Helper()
	v := agents.AgentView{Org: id.Org, Workspace: id.Workspace, Name: name, Provider: provider}
	if err := store.PutDoc(context.Background(), st, agents.CollectionAgents, store.Key(id.Org, id.Workspace, name), v); err != nil {
		t.Fatal(err)
	}
}

// TestStreamRunStreamsAndRecords proves the agent streaming path delivers deltas
// via onChunk AND records the terminal run (so it stays replay-stable): the
// streamed text aggregates to the recorded text, for both a true StreamingProvider
// and the non-streaming Stub fallback (which emits the full text as one chunk).
func TestStreamRunStreamsAndRecords(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	reg := ai.NewRegistry()
	reg.Register(ai.Stub{})
	reg.Register(fakeStream{})
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	h := command.NewHandler(log, st, reg)

	cases := []struct {
		name, provider string
		wantChunks     int // exact for the streaming provider; ">=1" asserted for the stub
		exactChunks    bool
	}{
		{"streaming provider", "fakestream", 3, true},
		{"stub fallback", "stub", 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			seedAgentProvider(t, st, id, c.name, c.provider)
			var chunks []string
			res, err := h.StreamRun(context.Background(), id, c.name, "hello", func(ch ai.Chunk) {
				chunks = append(chunks, ch.Text)
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != agentdomain.RunCompleted {
				t.Fatalf("status = %v (%s)", res.Status, res.Error)
			}
			if res.Text == "" {
				t.Fatal("recorded text is empty")
			}
			if len(chunks) == 0 || (c.exactChunks && len(chunks) != c.wantChunks) {
				t.Fatalf("got %d chunks, want %d (exact=%v)", len(chunks), c.wantChunks, c.exactChunks)
			}
			if strings.Join(chunks, "") != res.Text {
				t.Fatalf("streamed %q != recorded %q", strings.Join(chunks, ""), res.Text)
			}
			// The run is recorded as a terminal event (replay reads this, not the stream).
			if s, ok := terminalStatus(t, log, res.RunID); !ok || s != "completed" {
				t.Fatalf("run not recorded completed: %q ok=%v", s, ok)
			}
		})
	}
}

func TestStreamRunRejectsUnknownAgent(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	reg := ai.NewRegistry()
	reg.Register(ai.Stub{})
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	h := command.NewHandler(log, st, reg)
	if _, err := h.StreamRun(context.Background(), id, "ghost", "hi", func(ai.Chunk) {}); err == nil {
		t.Fatal("expected an error for an unknown agent")
	}
}
