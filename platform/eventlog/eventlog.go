// SPDX-License-Identifier: AGPL-3.0-or-later

// Package eventlog defines the append-only event log that is the system's
// backbone. Events are immutable; current state is a projection
// rebuilt from the log, enabling perfect replay and log-based rollback.
package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Envelope is the immutable, ordered record stored in the log. Streams are
// partitioned per (Org, Workspace) so replay/rollback is per-tenant.
type Envelope struct {
	ID        string          `json:"id"`
	Org       string          `json:"org"`
	Workspace string          `json:"workspace"`
	Stream    string          `json:"stream"`
	Type      string          `json:"type"`
	Time      time.Time       `json:"time"`
	Actor     string          `json:"actor"`
	Seq       uint64          `json:"seq"`
	Payload   json.RawMessage `json:"payload"`
	// Unique, when non-empty, is a tenant-global claim key the Append enforces as
	// unique across the whole log: a second Append carrying the same Unique fails
	// with ErrConflict. It is the optimistic-concurrency primitive that makes a
	// fold-then-append (e.g. "the next flow version is N", "this slug is free")
	// safe across PROCESSES, not just within one Handler's mutex — the loser of a
	// race is rejected and retries. Empty = no constraint (the common case).
	Unique string `json:"unique,omitempty"`
}

// ErrClosed is returned by a closed log.
var ErrClosed = errors.New("eventlog: closed")

// ErrConflict is returned by Append when the envelope's Unique key is already
// claimed — the caller lost an optimistic-concurrency race and should re-fold and
// retry (or report the duplicate, e.g. a taken slug).
var ErrConflict = errors.New("eventlog: unique key conflict")

// stampForAppend validates and stamps an envelope for the single-process log
// backends (WAL, memory): tenancy must be set, a Unique key already claimed in
// this process is ErrConflict, a missing ID is generated, and nextSeq is
// assigned. The claimed map is NOT updated here — the caller records the claim
// only once the append has succeeded.
func stampForAppend(e Envelope, claimed map[string]bool, nextSeq uint64) (Envelope, error) {
	if e.Org == "" || e.Workspace == "" {
		return Envelope{}, fmt.Errorf("eventlog: event %q missing org/workspace", e.Type)
	}
	if e.Unique != "" && claimed[e.Unique] {
		return Envelope{}, ErrConflict
	}
	if e.ID == "" {
		e.ID = newID()
	}
	e.Seq = nextSeq
	return e, nil
}

// nullableKey maps an empty claim key to SQL NULL so it is excluded from the
// partial unique index (an empty string would otherwise collide with every other
// unconstrained append). A non-empty key is stored verbatim.
func nullableKey(k string) any {
	if k == "" {
		return nil
	}
	return k
}

// Log is the append-only, ordered, replayable event store + in-process bus.
// Implementations must be safe for concurrent use.
// DefaultPollInterval is how often polling log implementations (sqlite) check
// for events appended by other processes (and themselves) to deliver to
// in-process subscribers.
const DefaultPollInterval = 200 * time.Millisecond

type Log interface {
	// Append assigns a monotonic Seq, persists the event durably, then
	// publishes it to subscribers. The stored envelope (with Seq) is returned.
	Append(ctx context.Context, e Envelope) (Envelope, error)
	// Read returns all events with Seq >= fromSeq in order (fromSeq 0 = all).
	Read(ctx context.Context, fromSeq uint64) ([]Envelope, error)
	// Subscribe returns a channel of events appended after subscription, plus
	// a cancel func. Used by the in-process projection runtime (the monolith).
	Subscribe() (<-chan Envelope, func())
	// Head returns the highest assigned Seq (0 when empty).
	Head() uint64
	Close() error
}

// AppendJSON marshals payload and appends it as a typed event for the given
// tenant + stream — the shared write-side spine that command handlers wrap with
// their stream constant and clock.
func AppendJSON(ctx context.Context, log Log, org, workspace, actor, stream, typ string, at time.Time, payload any) (Envelope, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("eventlog: marshal %s: %w", typ, err)
	}
	return log.Append(ctx, Envelope{
		Org: org, Workspace: workspace, Actor: actor,
		Stream: stream, Type: typ, Time: at, Payload: b,
	})
}
