// SPDX-License-Identifier: AGPL-3.0-or-later

// Package eventlog defines the append-only event log that is the system's
// backbone. Events are immutable; current state is a projection
// rebuilt from the log, enabling perfect replay and log-based rollback.
package eventlog

import (
	"context"
	"encoding/json"
	"errors"
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
}

// ErrClosed is returned by a closed log.
var ErrClosed = errors.New("eventlog: closed")

// Log is the append-only, ordered, replayable event store + in-process bus.
// Implementations must be safe for concurrent use.
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
