// SPDX-License-Identifier: AGPL-3.0-or-later

package eventlog_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
)

// ReadStream selects one stream across every tenant. Decision recovery needs
// exactly this: it cannot know which tenant holds an interrupted decision, so
// ReadTenantStream was unusable and it read the entire log instead -- decrypting
// 345,970 events once a second on a live deployment to look for events on a
// stream that held none of them.
//
// Every backend must answer identically, because the caller picks its log by
// deployment profile and must not get different recovery behaviour from that
// choice. The expectation is written as a full read filtered by hand, so the
// indexed SQL implementations are checked against the meaning of the operation
// rather than against a hard-coded list.
func TestReadStreamSelectsOneStreamAcrossTenants(t *testing.T) {
	backends := map[string]func(t *testing.T) eventlog.Log{
		"memory": func(t *testing.T) eventlog.Log { return eventlog.NewMemory() },
		"sqlite": func(t *testing.T) eventlog.Log {
			l, err := eventlog.OpenSQLiteLog(t.TempDir(), 10*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			return l
		},
		"wal": func(t *testing.T) eventlog.Log {
			l, err := eventlog.OpenWAL(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return l
		},
	}
	for name, open := range backends {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			log := open(t)
			defer func() { _ = log.Close() }()

			ev := func(org, ws, stream, typ string) eventlog.Envelope {
				return eventlog.Envelope{
					Org: org, Workspace: ws, Actor: "d", Stream: stream, Type: typ,
					Time: time.Unix(0, 0).UTC(), Payload: json.RawMessage(`{}`),
				}
			}
			// Two tenants on the wanted stream, plus a high-volume stream around
			// them: the shape that made the full-log read so expensive.
			for _, e := range []eventlog.Envelope{
				ev("o1", "w", "platform.leader", "claim"),
				ev("o1", "w", "decisions", "started"),
				ev("o2", "w", "platform.leader", "claim"),
				ev("o2", "w2", "decisions", "resumed"),
				ev("o1", "w", "platform.leader", "claim"),
			} {
				if _, err := log.Append(ctx, e); err != nil {
					t.Fatal(err)
				}
			}

			got, err := log.ReadStream(ctx, "decisions", 0)
			if err != nil {
				t.Fatalf("ReadStream() error = %v", err)
			}
			all, err := log.Read(ctx, 0)
			if err != nil {
				t.Fatal(err)
			}
			var want []eventlog.Envelope
			for _, e := range all {
				if e.Stream == "decisions" {
					want = append(want, e)
				}
			}
			if len(got) != len(want) {
				t.Fatalf("ReadStream() returned %d events, want %d", len(got), len(want))
			}
			for i := range got {
				if got[i].Seq != want[i].Seq || got[i].Org != want[i].Org {
					t.Errorf("event %d = seq %d org %q, want seq %d org %q",
						i, got[i].Seq, got[i].Org, want[i].Seq, want[i].Org)
				}
				if got[i].Stream != "decisions" {
					t.Errorf("event %d leaked stream %q", i, got[i].Stream)
				}
			}
			// Both tenants, or the cross-tenant part of the contract is untested.
			if len(got) == 2 && got[0].Org == got[1].Org {
				t.Error("ReadStream() returned one tenant only")
			}
		})
	}
}

// A stream with no events answers empty rather than falling back to everything.
// Precisely the deployed case -- zero decision events against 345,970 others --
// so a backend that quietly widened the read would reintroduce the whole bug.
func TestReadStreamOnAnAbsentStreamReturnsNothing(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	defer func() { _ = log.Close() }()
	for i := 0; i < 50; i++ {
		if _, err := log.Append(ctx, eventlog.Envelope{
			Org: "o", Workspace: "w", Actor: "d", Stream: "platform.leader", Type: "claim",
			Time: time.Unix(0, 0).UTC(), Payload: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := log.ReadStream(ctx, "decisions", 0)
	if err != nil {
		t.Fatalf("ReadStream() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ReadStream() on an absent stream returned %d events, want 0", len(got))
	}
}
