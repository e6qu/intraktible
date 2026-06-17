// SPDX-License-Identifier: AGPL-3.0-or-later

package monitor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// Handler is the monitor write side (imperative shell).
type Handler struct {
	log   eventlog.Log
	now   func() time.Time
	newID func() string
}

// NewHandler builds a Handler using the system clock and a random id source.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }, newID: newID}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// DefineCmd defines a monitor on a flow.
type DefineCmd struct {
	FlowID      string
	Metric      string
	Op          string
	Threshold   float64
	Description string
}

// Define records a Defined event after validating the rule.
func (h *Handler) Define(ctx context.Context, id identity.Identity, cmd DefineCmd) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if cmd.FlowID == "" {
		return "", eventlog.Envelope{}, fmt.Errorf("monitor: flow_id is required")
	}
	if !ValidMetric(cmd.Metric) {
		return "", eventlog.Envelope{}, fmt.Errorf("monitor: invalid metric %q", cmd.Metric)
	}
	if !ValidOp(cmd.Op) {
		return "", eventlog.Envelope{}, fmt.Errorf("monitor: invalid op %q (gt|lt)", cmd.Op)
	}
	mid := h.newID()
	e, err := h.append(ctx, id, TypeDefined, Defined{
		MonitorID: mid, FlowID: cmd.FlowID, Metric: cmd.Metric,
		Op: cmd.Op, Threshold: cmd.Threshold, Description: cmd.Description,
	})
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	return mid, e, nil
}

// Delete records a Deleted event for a monitor.
func (h *Handler) Delete(ctx context.Context, id identity.Identity, flowID, monitorID string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if monitorID == "" {
		return eventlog.Envelope{}, fmt.Errorf("monitor: monitor_id is required")
	}
	return h.append(ctx, id, TypeDeleted, Deleted{MonitorID: monitorID, FlowID: flowID})
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ string, payload any) (eventlog.Envelope, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("monitor: marshal %s: %w", typ, err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: StreamMonitors, Type: typ, Time: h.now(), Payload: b,
	})
}
