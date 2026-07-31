// SPDX-License-Identifier: AGPL-3.0-or-later

// Package leader provides epoch-based leader/work claims for redundant live
// replicas. A claim is an event-log unique fact: exactly one replica wins a
// role's claim for a given epoch, so concurrent replicas elect a single leader
// deterministically on every supported event-log backend (memory, WAL, SQLite,
// Postgres, NATS) without a separate lease store.
package leader

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// StreamLeader is the leader-claim event stream.
const StreamLeader = "platform.leader"

// TypeLeaderClaimed is the single leader-claim event type.
const TypeLeaderClaimed = "platform.leader.claimed"

// Claimed records that holder won role's claim for epoch.
type Claimed struct {
	Role      string    `json:"role"`
	Epoch     int64     `json:"epoch"`
	Holder    string    `json:"holder"`
	ClaimedAt time.Time `json:"claimed_at"`
}

// EpochFor returns the epoch containing at for the given window. Epochs are
// deterministic across replicas (they derive from the shared clock, not a local
// counter), so all replicas agree on which epoch a tick belongs to.
func EpochFor(at time.Time, window time.Duration) int64 {
	return at.UnixNano() / window.Nanoseconds()
}

// Claim attempts to make holder the leader of role for epoch. It returns true
// when this replica won the epoch's claim, false when another replica already
// holds it. The win is durable and replayable: exactly one replica can ever
// hold a given (role, epoch) claim.
//
// The claim uses a tenant-global unique key so concurrent replicas racing the
// same epoch produce exactly one winner; the loser sees ErrConflict and yields.
func Claim(
	ctx context.Context,
	log eventlog.Log,
	id identity.Identity,
	role string,
	epoch int64,
	holder string,
	at time.Time,
) (bool, error) {
	if role == "" || holder == "" {
		return false, errors.New("leader: role and holder are required")
	}
	_, err := eventlog.AppendJSONUnique(
		ctx, log, id.Org, id.Workspace, id.Actor,
		StreamLeader, TypeLeaderClaimed, at,
		Claimed{Role: role, Epoch: epoch, Holder: holder, ClaimedAt: at},
		id.Org+"\x00"+id.Workspace+"\x00leader\x00"+role+"\x00"+fmt.Sprint(epoch),
	)
	if err != nil {
		if errors.Is(err, eventlog.ErrConflict) {
			return false, nil
		}
		return false, fmt.Errorf("leader: claim %s epoch %d: %w", role, epoch, err)
	}
	return true, nil
}

// TryClaim is Claim using the current time, computing the epoch from now and
// the window. It is the convenience the scheduler loop uses.
func TryClaim(
	ctx context.Context,
	log eventlog.Log,
	id identity.Identity,
	role string,
	holder string,
	window time.Duration,
	now func() time.Time,
) (bool, error) {
	at := now()
	return Claim(ctx, log, id, role, EpochFor(at, window), holder, at)
}
