// SPDX-License-Identifier: AGPL-3.0-or-later

package preapproval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// Handler is the pre-approval write side (imperative shell).
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

// GrantCmd grants a pre-approval for an entity, valid for ValidDays.
type GrantCmd struct {
	EntityType    string
	EntityID      string
	Disposition   string // approve | decline (default approve)
	Terms         json.RawMessage
	PolicyID      string
	PolicyVersion int
	FlowSlug      string
	ValidDays     int
	Note          string
}

// Grant records a Granted event, computing ValidUntil from the clock (an effect,
// so it is recorded and replay-stable).
func (h *Handler) Grant(ctx context.Context, id identity.Identity, cmd GrantCmd) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if cmd.EntityType == "" || cmd.EntityID == "" {
		return "", eventlog.Envelope{}, fmt.Errorf("preapproval: entity_type and entity_id are required")
	}
	// A pre-approval pre-decides a policy disposition (the shared policy.Disposition
	// vocabulary) — but only the terminal approve/decline, never refer (you cannot
	// pre-refer to a human). Default to approve.
	disp := policy.Disposition(cmd.Disposition)
	if disp == "" {
		disp = policy.Approve
	}
	if disp != policy.Approve && disp != policy.Decline {
		return "", eventlog.Envelope{}, fmt.Errorf("preapproval: invalid disposition %q (approve|decline)", disp)
	}
	if cmd.ValidDays <= 0 {
		return "", eventlog.Envelope{}, fmt.Errorf("preapproval: valid_days must be positive")
	}
	// Cap the horizon so the duration multiply below cannot overflow int64 ns into a
	// negative/garbage ValidUntil (10 years is well beyond any real pre-approval).
	if cmd.ValidDays > 3650 {
		return "", eventlog.Envelope{}, fmt.Errorf("preapproval: valid_days too large (max 3650)")
	}
	id2 := h.newID()
	e, err := h.append(ctx, id, TypeGranted, Granted{
		PreApprovalID: id2, EntityType: cmd.EntityType, EntityID: cmd.EntityID,
		Disposition: string(disp), Terms: cmd.Terms, PolicyID: cmd.PolicyID, PolicyVersion: cmd.PolicyVersion,
		FlowSlug: cmd.FlowSlug, ValidUntil: h.now().Add(time.Duration(cmd.ValidDays) * 24 * time.Hour),
		Note: cmd.Note,
	})
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	return id2, e, nil
}

// Revoke invalidates an entity's current pre-approval.
func (h *Handler) Revoke(ctx context.Context, id identity.Identity, entityType, entityID, reason string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if entityType == "" || entityID == "" {
		return eventlog.Envelope{}, fmt.Errorf("preapproval: entity_type and entity_id are required")
	}
	return h.append(ctx, id, TypeRevoked, Revoked{EntityType: entityType, EntityID: entityID, Reason: reason})
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ string, payload any) (eventlog.Envelope, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("preapproval: marshal %s: %w", typ, err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: StreamPreApprovals, Type: typ, Time: h.now(), Payload: b,
	})
}
