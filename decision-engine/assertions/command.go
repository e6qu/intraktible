// SPDX-License-Identifier: AGPL-3.0-or-later

package assertions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// maxCases caps the number of assertion cases stored per flow.
const maxCases = 200

// Handler is the assertions write side (imperative shell).
type Handler struct {
	log eventlog.Log
	now func() time.Time
}

// NewHandler builds a Handler using the system clock.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the clock used to stamp recorded events (deterministic
// tests, the demo seeder) and returns the handler.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// SetCases replaces a flow's assertion cases (each must have a non-empty name).
func (h *Handler) SetCases(ctx context.Context, id identity.Identity, flowID string, cases []Case) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if flowID == "" {
		return eventlog.Envelope{}, fmt.Errorf("assertions: flow_id is required")
	}
	if len(cases) > maxCases {
		return eventlog.Envelope{}, fmt.Errorf("assertions: too many cases (%d > %d)", len(cases), maxCases)
	}
	for i := range cases {
		cases[i].Name = strings.TrimSpace(cases[i].Name)
		if cases[i].Name == "" {
			return eventlog.Envelope{}, fmt.Errorf("assertions: case %d needs a name", i)
		}
	}
	b, err := json.Marshal(Set{FlowID: flowID, Cases: cases})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("assertions: marshal cases: %w", err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: StreamAssertions, Type: TypeSet, Time: h.now(), Payload: b,
	})
}
