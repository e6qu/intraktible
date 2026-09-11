// SPDX-License-Identifier: AGPL-3.0-or-later

package projection

import (
	"context"
	"testing"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/store"
)

// gappedLog serves a history with a hole in it, which is what a machine that lost
// power leaves behind: BIGSERIAL hands out a seq at INSERT and does not give it
// back when the transaction dies, so the numbers in the hole are burned for good.
type gappedLog struct {
	eventlog.Log
	events []eventlog.Envelope
}

func (l *gappedLog) Read(_ context.Context, fromSeq uint64) ([]eventlog.Envelope, error) {
	var out []eventlog.Envelope
	for _, e := range l.events {
		if e.Seq >= fromSeq {
			out = append(out, e)
		}
	}
	return out, nil
}

func TestGapIsPermanentOnlyWhenTheLogHoldsNothingInIt(t *testing.T) {
	ctx := context.Background()

	// Seqs 3 and 4 were burned by a crash: the log jumps 2 -> 5.
	burned := &Runtime{
		log: &gappedLog{events: []eventlog.Envelope{
			{Seq: 1, Type: "a"}, {Seq: 2, Type: "b"}, {Seq: 5, Type: "e"},
		}},
		store: store.NewMemory(),
	}
	settled, err := burned.gapIsPermanent(ctx, 2, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !settled {
		t.Fatal("a gap the log holds nothing in was not treated as permanent, so the projection stays wedged on events that can never arrive")
	}

	// The same shape, except seq 3 really is there: the poller still owes it, and
	// advancing over it would drop an event that exists.
	pending := &Runtime{
		log: &gappedLog{events: []eventlog.Envelope{
			{Seq: 1, Type: "a"}, {Seq: 2, Type: "b"}, {Seq: 3, Type: "c"}, {Seq: 5, Type: "e"},
		}},
		store: store.NewMemory(),
	}
	settled, err = pending.gapIsPermanent(ctx, 2, 5)
	if err != nil {
		t.Fatal(err)
	}
	if settled {
		t.Fatal("a gap containing a real unapplied event was called permanent; advancing over it would skip seq 3")
	}
}
