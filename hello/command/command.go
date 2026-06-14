// SPDX-License-Identifier: AGPL-3.0-or-later

// Package command is the hello feature's write side (imperative shell): it
// validates via the functional core, then appends an event to the log.
package command

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/hello/domain"
	"github.com/e6qu/intraktible/hello/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// Handler records greetings as events.
type Handler struct {
	log eventlog.Log
	now func() time.Time
}

// NewHandler builds a Handler using the system clock.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }}
}

// SayHello validates the command and appends a HelloRecorded event scoped to id.
func (h *Handler) SayHello(ctx context.Context, id identity.Identity, cmd domain.SayHello) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	payload, err := json.Marshal(domain.Record(cmd))
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("hello: marshal event: %w", err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org:       id.Org,
		Workspace: id.Workspace,
		Actor:     id.Actor,
		Stream:    events.Stream,
		Type:      events.TypeHelloRecorded,
		Time:      h.now(),
		Payload:   payload,
	})
}
